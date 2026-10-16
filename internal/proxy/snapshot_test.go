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
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rendercache/rendercache/internal/config"
	"github.com/rendercache/rendercache/internal/render"
	"github.com/rendercache/rendercache/internal/snapshot"
	"github.com/rendercache/rendercache/internal/target"
	"github.com/rendercache/rendercache/internal/warm"
)

type sourceFunc func(context.Context) ([]target.Page, error)

func (f sourceFunc) Discover(ctx context.Context) ([]target.Page, error) { return f(ctx) }

type engineFunc func(context.Context, target.Page) (render.Result, error)

func (f engineFunc) Render(ctx context.Context, p target.Page) (render.Result, error) {
	return f(ctx, p)
}
func cachedGateway(t *testing.T) (*Gateway, *snapshot.Store, string, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Set-Cookie", "origin=private")
		io.WriteString(w, "origin shell")
	}))
	t.Cleanup(origin.Close)
	c := config.Default()
	c.Origin = origin.URL
	c.PublicURL = "https://public.example"
	c.SnapshotTTL = "1s"
	c.AdminToken = "secret"
	g, err := New(c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	dir := t.TempDir()
	store, _ := snapshot.Open(dir, 1<<20)
	g.SetSnapshotStore(store)
	return g, store, dir, &calls
}
func mapped(g *Gateway, path string) target.Page {
	p, _ := target.Map(g.cfg.SiteURL(), g.cfg.Origin, path)
	return p
}
func bot(g *Gateway, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://spoofed.test"+path, nil)
	r.Header.Set("User-Agent", "GPTBot")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}
func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out")
}
func attach(t *testing.T, g *Gateway, s *snapshot.Store, engine engineFunc, extra int) *warm.Manager {
	t.Helper()
	m, err := warm.New(warm.Options{Site: g.cfg.SiteURL(), Origin: g.cfg.Origin, RefreshInterval: time.Hour, DiscoveryInterval: time.Hour, RetryBase: 50 * time.Millisecond, RetryMax: time.Second, Workers: 1, QueueSize: 1, MaxPages: 10, MaxExtraPages: extra, Tick: time.Millisecond}, sourceFunc(func(context.Context) ([]target.Page, error) { return nil, nil }), engine, s)
	if err != nil {
		t.Fatal(err)
	}
	g.SetWarmer(m)
	m.Start(context.Background())
	t.Cleanup(m.Close)
	return m
}
func TestFreshConditionalHeadAndPrivacyBypass(t *testing.T) {
	g, s, _, calls := cachedGateway(t)
	p := mapped(g, "/page?q=1")
	record, err := s.Save(p, render.Result{HTML: "<h1>snapshot</h1>", Status: 200, Headers: map[string]string{"X-Robots-Tag": "noindex", "Content-Language": "en"}})
	if err != nil {
		t.Fatal(err)
	}
	w := bot(g, "GET", "/page?q=1", nil)
	if w.Code != 200 || w.Body.String() != "<h1>snapshot</h1>" || w.Header().Get("X-RenderCache") != "HIT" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("X-Robots-Tag") != "noindex" || calls.Load() != 0 {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	for _, method := range []string{"GET", "HEAD"} {
		w = bot(g, method, "/page?q=1", map[string]string{"If-None-Match": "W/" + record.ETag()})
		if w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "" {
			t.Fatal(w.Code, w.Header())
		}
	}
	w = bot(g, "HEAD", "/page?q=1", nil)
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != strconv.Itoa(len(record.Result.HTML)) {
		t.Fatal(w.Code, w.Header())
	}
	w = bot(g, "GET", "/page?q=1", map[string]string{"If-Modified-Since": record.CreatedAt.UTC().Format(http.TimeFormat)})
	if w.Code != 304 {
		t.Fatal(w.Code)
	}
	w = bot(g, "GET", "/page?q=1", map[string]string{"If-None-Match": "\"different\"", "If-Modified-Since": time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)})
	if w.Code != 200 {
		t.Fatal("If-None-Match did not take precedence")
	}
	for _, headers := range []map[string]string{{"Cookie": "session=1"}, {"Authorization": "Bearer private"}, {"Range": "bytes=0-2"}, {"Cache-Control": "no-cache"}, {"If-Match": "*"}, {"User-Agent": "Mozilla/5.0"}, {"Accept": "application/json"}} {
		w = bot(g, "GET", "/page?q=1", headers)
		if w.Body.String() != "origin shell" || w.Header().Get("X-RenderCache") != "BYPASS" {
			t.Fatal("private/non-document request received snapshot", w.Header())
		}
	}
}
func TestStaleIsImmediateAndRefreshDeduplicated(t *testing.T) {
	g, s, dir, _ := cachedGateway(t)
	p := mapped(g, "/page")
	rec, _ := s.Save(p, render.Result{HTML: "old", Status: 200})
	rec.CreatedAt = time.Now().Add(-time.Hour)
	data, _ := json.Marshal(rec)
	os.WriteFile(filepath.Join(dir, p.Key()+".json"), data, 0600)
	gate := make(chan struct{})
	var renders atomic.Int32
	attach(t, g, s, func(ctx context.Context, p target.Page) (render.Result, error) {
		renders.Add(1)
		select {
		case <-gate:
		case <-ctx.Done():
			return render.Result{}, ctx.Err()
		}
		return render.Result{HTML: "new", Status: 200}, nil
	}, 2)
	for range 30 {
		start := time.Now()
		w := bot(g, "GET", "/page", nil)
		if w.Body.String() != "old" || w.Header().Get("X-RenderCache") != "STALE" || time.Since(start) > 500*time.Millisecond {
			t.Fatal("stale request waited for renderer", w.Body.String())
		}
	}
	waitFor(t, func() bool { return renders.Load() == 1 })
	close(gate)
	waitFor(t, func() bool { r, e := s.Load(p); return e == nil && r.Result.HTML == "new" })
	w := bot(g, "GET", "/page", nil)
	if w.Body.String() != "new" || w.Header().Get("X-RenderCache") != "HIT" || renders.Load() != 1 {
		t.Fatal(w.Body.String(), renders.Load())
	}
}
func TestColdCapacityAndPurgeAuthentication(t *testing.T) {
	g, s, _, _ := cachedGateway(t)
	gate := make(chan struct{})
	var renders atomic.Int32
	m := attach(t, g, s, func(ctx context.Context, p target.Page) (render.Result, error) {
		renders.Add(1)
		select {
		case <-gate:
		case <-ctx.Done():
			return render.Result{}, ctx.Err()
		}
		return render.Result{HTML: "warmed", Status: 200}, nil
	}, 1)
	for range 10 {
		w := bot(g, "GET", "/cold", nil)
		if w.Header().Get("X-RenderCache") != "MISS" || w.Body.String() != "origin shell" {
			t.Fatal(w.Header())
		}
	}
	waitFor(t, func() bool { return renders.Load() == 1 })
	bot(g, "GET", "/overflow", nil)
	if m.Status().ExtraPages != 1 || m.Status().RequestRejections == 0 {
		t.Fatal(m.Status())
	}
	purge := func(method, raw, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://admin/purge?"+raw, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		g.AdminHandler().ServeHTTP(w, req)
		return w
	}
	if purge("POST", "url=/cold", "wrong").Code != 401 || purge("GET", "url=/cold", "secret").Code != 405 {
		t.Fatal("purge authentication/method failed")
	}
	for _, raw := range []string{"", "url=/a&url=/b", "url=https://foreign.example/", "url=/api/page", "url=/asset.js", "url=%ZZ"} {
		if w := purge("POST", raw, "secret"); w.Code != 400 {
			t.Fatal(raw, w.Code)
		}
	}
	if purge("POST", "url=/overflow", "secret").Code != 429 {
		t.Fatal("capacity rejection not exposed")
	}
	if purge("POST", "url=/cold", "secret").Code != 202 {
		t.Fatal("coalesced purge rejected")
	}
	close(gate)
	waitFor(t, func() bool { return m.Status().RenderSuccesses >= 2 }) // purge during render schedules one follow-up
	g.cfg.AdminToken = ""
	if purge("POST", "url=/cold", "").Code != 503 {
		t.Fatal("purge allowed without configured token")
	}
}
func TestFailedRefreshKeepsOldAndPrivateWithdraws(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(strconv.FormatBool(private), func(t *testing.T) {
			g, s, _, _ := cachedGateway(t)
			p := mapped(g, "/page")
			s.Save(p, render.Result{HTML: "old", Status: 200})
			engineErr := errors.New("temporary 500")
			if private {
				engineErr = render.ErrPrivate
			}
			m := attach(t, g, s, func(context.Context, target.Page) (render.Result, error) { return render.Result{}, engineErr }, 2)
			m.Request(p, "purge")
			waitFor(t, func() bool { return m.Status().RenderFailures >= 1 })
			w := bot(g, "GET", "/page", nil)
			if private {
				if w.Body.String() != "origin shell" || w.Header().Get("X-RenderCache") != "MISS" {
					t.Fatal("withdrawn private snapshot served")
				}
				if _, err := s.Load(p); !errors.Is(err, snapshot.ErrNotFound) {
					t.Fatal(err)
				}
			} else if w.Body.String() != "old" {
				t.Fatal("transient failure destroyed snapshot")
			}
		})
	}
}
func TestRedirectRemovalAndCorruptFileFallback(t *testing.T) {
	g, s, dir, _ := cachedGateway(t)
	for _, tc := range []struct {
		path string
		out  render.Result
	}{{"/redirect", render.Result{Status: 302, Location: "https://other.example/new#part"}}, {"/gone", render.Result{Status: 410}}, {"/missing", render.Result{Status: 404}}} {
		p := mapped(g, tc.path)
		if _, err := s.Save(p, tc.out); err != nil {
			t.Fatal(err)
		}
		w := bot(g, "GET", tc.path, nil)
		if w.Code != tc.out.Status || w.Header().Get("Location") != tc.out.Location || w.Body.Len() != 0 {
			t.Fatal(w.Code, w.Header())
		}
	}
	p := mapped(g, "/corrupt")
	rec, _ := s.Save(p, render.Result{HTML: "old", Status: 200})
	os.WriteFile(filepath.Join(dir, p.Key()+".json"), []byte("bad json"), 0600)
	w := bot(g, "GET", "/corrupt", nil)
	if w.Header().Get("X-RenderCache") != "MISS" {
		t.Fatal(w.Header())
	}
	// Legacy followed redirects must be re-rendered, never served as a 200 document.
	rec.Result.Redirects = []render.Redirect{{URL: p.Origin, Status: 302}}
	b, _ := json.Marshal(rec)
	os.WriteFile(filepath.Join(dir, p.Key()+".json"), b, 0600)
	if _, err := s.Load(p); err == nil {
		t.Fatal("legacy followed redirect accepted")
	}
}
