// SPDX-License-Identifier: Apache-2.0
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rendercache/rendercache/internal/config"
	"github.com/rendercache/rendercache/internal/render"
	"github.com/rendercache/rendercache/internal/snapshot"
	"github.com/rendercache/rendercache/internal/target"
)

// gatewayWithOrigin is cachedGateway with an origin the test can shut down.
func gatewayWithOrigin(t *testing.T) (*Gateway, *snapshot.Store, string, *httptest.Server) {
	t.Helper()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "origin shell") }))
	t.Cleanup(origin.Close)
	c := config.Default()
	c.Origin = origin.URL
	c.PublicURL = "https://public.example"
	c.SnapshotTTL = "1s"
	g, err := New(c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	dir := t.TempDir()
	store, _ := snapshot.Open(dir, 1<<20)
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	}) // Windows cannot delete a folder still held open
	g.SetSnapshotStore(store)
	return g, store, dir, origin
}

// saveStale writes a snapshot that is already past its TTL.
func saveStale(t *testing.T, g *Gateway, s *snapshot.Store, dir, path, html string) target.Page {
	t.Helper()
	p := mapped(g, path)
	rec, err := s.Save(p, render.Result{HTML: html, Status: 200})
	if err != nil {
		t.Fatal(err)
	}
	rec.CreatedAt = time.Now().Add(-time.Hour)
	data, _ := json.Marshal(rec)
	if err = os.WriteFile(filepath.Join(dir, p.Key()+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

// With the origin down and every refresh failing, crawlers keep getting the
// saved copy and the failed refreshes never delete it.
func TestOriginDownStillServesSavedPages(t *testing.T) {
	g, s, dir, origin := gatewayWithOrigin(t)
	p := saveStale(t, g, s, dir, "/article", "<h1>saved article</h1>")
	origin.Close()
	var attempts atomic.Int32
	attach(t, g, s, func(ctx context.Context, p target.Page) (render.Result, error) {
		attempts.Add(1)
		return render.Result{}, errors.New("origin unreachable")
	}, 4)
	const requests = 50
	served := 0
	for range requests {
		w := bot(g, "GET", "/article", nil)
		if w.Code == 200 && w.Body.String() == "<h1>saved article</h1>" && w.Header().Get("X-RenderCache") == "STALE" {
			served++
		}
	}
	// Let the background refresh fail and retry a few times before checking.
	waitFor(t, func() bool { return attempts.Load() >= 3 })
	if record, err := s.Load(p); err != nil || record.Result.HTML != "<h1>saved article</h1>" {
		t.Fatal("failed refresh removed or changed the saved copy", err)
	}
	if w := bot(g, "GET", "/article", nil); w.Body.String() != "<h1>saved article</h1>" {
		t.Fatal("saved copy not served after failed refreshes")
	}
	human := httptest.NewRecorder()
	g.ServeHTTP(human, httptest.NewRequest("GET", "http://public.test/article", nil))
	if human.Code != http.StatusBadGateway {
		t.Fatal("visitors should see the origin failure, got", human.Code)
	}
	if served != requests {
		t.Fatalf("served %d of %d crawler requests from disk", served, requests)
	}
	t.Logf("RESULT origin_down_crawler_requests=%d", requests)
	t.Logf("RESULT origin_down_served_from_saved_copy=%d", served)
}

// A crawler never waits for Chrome: with the renderer stuck, stale pages come
// from disk and cold pages from the origin, both immediately.
func TestCrawlerNeverWaitsForBrowser(t *testing.T) {
	g, s, dir, _ := gatewayWithOrigin(t)
	saveStale(t, g, s, dir, "/saved", "<h1>saved</h1>")
	stuck := make(chan struct{})
	entered := make(chan struct{}, 1)
	t.Cleanup(func() { close(stuck) })
	attach(t, g, s, func(ctx context.Context, p target.Page) (render.Result, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-stuck:
		case <-ctx.Done():
		}
		return render.Result{}, ctx.Err()
	}, 8)
	// Start a render and wait until Chrome is stuck in it before measuring.
	bot(g, "GET", "/saved", nil)
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("renderer never started")
	}
	const requests = 100
	var slowest time.Duration
	for i := range requests {
		path, want, outcome := "/saved", "<h1>saved</h1>", "STALE"
		if i%2 == 1 {
			path, want, outcome = "/cold", "origin shell", "MISS"
		}
		start := time.Now()
		w := bot(g, "GET", path, nil)
		elapsed := time.Since(start)
		if w.Body.String() != want || w.Header().Get("X-RenderCache") != outcome {
			t.Fatalf("%s: got %q %s", path, w.Body.String(), w.Header().Get("X-RenderCache"))
		}
		slowest = max(slowest, elapsed)
	}
	if slowest > time.Second {
		t.Fatal("a crawler request waited for the renderer:", slowest)
	}
	t.Logf("RESULT crawler_requests_while_chrome_stuck=%d", requests)
	t.Logf("RESULT slowest_crawler_response_ms_while_chrome_stuck=%d", slowest.Milliseconds())
}
