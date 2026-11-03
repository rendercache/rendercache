// SPDX-License-Identifier: Apache-2.0
package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rendercache/rendercache/internal/config"
	"github.com/rendercache/rendercache/internal/delivery"
	"github.com/rendercache/rendercache/internal/render"
	"github.com/rendercache/rendercache/internal/snapshot"
	"github.com/rendercache/rendercache/internal/verify"
)

// verifiedGateway serves a saved /pricing page, with verification on and
// GPTBot's published range 203.0.113.0/24 (fetched from a local server).
func verifiedGateway(t *testing.T) (*Gateway, *snapshot.Store, func() int32) {
	t.Helper()
	g, store, _, calls := cachedGateway(t)
	g.edit(func(c *config.Config) {
		c.SnapshotTTL = "1h"
		c.VerifyCrawlers = true
		c.TrustedProxies = []string{"10.0.0.0/8"}
	})
	ranges := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"creationTime":"2026-11-03","prefixes":[{"ipv4Prefix":"203.0.113.0/24"},{"ipv6Prefix":"2001:db8:4::/48"}]}`))
	}))
	t.Cleanup(ranges.Close)
	v, err := verify.New(verify.Options{Sources: []verify.Source{{Crawler: "GPTBot", RangesURL: ranges.URL + "/gptbot.json"}}, Client: ranges.Client(), Resolver: noDNS{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	g.SetVerifier(v)
	if _, err := store.Save(mapped(g, "/pricing"), render.Result{HTML: "<h1>Pricing</h1>", Status: 200}); err != nil {
		t.Fatal(err)
	}
	return g, store, calls.Load
}

type noDNS struct{ verify.Resolver }

func fetch(g *Gateway, ua, remote string, forwarded ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://public.test/pricing", nil)
	r.Header.Set("User-Agent", ua)
	r.RemoteAddr = remote
	for _, f := range forwarded {
		r.Header.Add("X-Forwarded-For", f)
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}

// The 0.3 promise: a request that only claims to be GPTBot gets the live
// site, and GPTBot from its published range gets the saved page.
func TestSpoofedCrawlerGetsLiveSite(t *testing.T) {
	g, _, originCalls := verifiedGateway(t)
	const gpt = "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2; +https://openai.com/gptbot)"
	cases := []struct {
		name, remote string
		forwarded    []string
		want         string
	}{
		{"from its range", "203.0.113.9:4000", nil, "HIT"},
		{"IPv6 in its range", "[2001:db8:4::7]:4000", nil, "HIT"},
		{"spoofed", "198.51.100.7:4000", nil, "BYPASS"},
		{"spoofed, forging X-Forwarded-For", "198.51.100.7:4000", []string{"203.0.113.9"}, "BYPASS"},
		{"through a trusted proxy", "10.1.2.3:4000", []string{"203.0.113.9"}, "HIT"},
		{"through two trusted proxies", "10.1.2.3:4000", []string{"203.0.113.9, 10.9.9.9"}, "HIT"},
		{"spoofer behind a trusted proxy", "10.1.2.3:4000", []string{"203.0.113.9, 198.51.100.7"}, "BYPASS"},
		{"malformed chain", "10.1.2.3:4000", []string{"203.0.113.9, junk"}, "BYPASS"},
		{"trusted proxy, no chain", "10.1.2.3:4000", nil, "BYPASS"},
		{"claims GPTBot and Slackbot", "198.51.100.7:4000", nil, "BYPASS"},
	}
	for _, c := range cases {
		ua := gpt
		if strings.Contains(c.name, "Slackbot") {
			ua = "Slackbot-LinkExpanding 1.0 " + gpt
		}
		before := originCalls()
		w := fetch(g, ua, c.remote, c.forwarded...)
		if got := w.Header().Get("X-RenderCache"); got != c.want {
			t.Errorf("%s: X-RenderCache %q, want %q", c.name, got, c.want)
		}
		live := originCalls() > before
		if live != (c.want == "BYPASS") || (c.want == "BYPASS") != (w.Body.String() == "origin shell") {
			t.Errorf("%s: live site %v, body %q", c.name, live, w.Body.String())
		}
	}

	// Off, the same spoofed claim gets the saved page, as in 0.2.
	g.edit(func(c *config.Config) { c.VerifyCrawlers = false })
	if got := fetch(g, gpt, "198.51.100.7:4000").Header().Get("X-RenderCache"); got != "HIT" {
		t.Errorf("verification off: %s", got)
	}
}

func TestUnverifiableCrawlers(t *testing.T) {
	g, _, _ := verifiedGateway(t)
	const slack = "Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)"
	if got := fetch(g, slack, "198.51.100.7:4000").Header().Get("X-RenderCache"); got != "HIT" {
		t.Errorf("snapshot policy: %s", got)
	}
	g.edit(func(c *config.Config) { c.UnverifiableCrawlers = "live" })
	if got := fetch(g, slack, "198.51.100.7:4000").Header().Get("X-RenderCache"); got != "BYPASS" {
		t.Errorf("live policy: %s", got)
	}
	// Verification on with no verifier attached never trusts a claim.
	g.edit(func(c *config.Config) { c.UnverifiableCrawlers = "snapshot" })
	g.SetVerifier(nil)
	if got := fetch(g, "GPTBot", "203.0.113.9:4000").Header().Get("X-RenderCache"); got != "BYPASS" {
		t.Errorf("no verifier: %s", got)
	}
}

func TestDeliveryLogRecordsCheck(t *testing.T) {
	g, _, _ := verifiedGateway(t)
	dir := t.TempDir()
	log, err := delivery.Open(filepath.Join(dir, "delivery"), 1<<20, 1)
	if err != nil {
		t.Fatal(err)
	}
	g.SetDeliveryLog(log)
	fetch(g, "GPTBot", "203.0.113.9:4000")
	fetch(g, "GPTBot", "198.51.100.7:4000")
	fetch(g, "Slackbot", "198.51.100.7:4000")
	if err := g.FlushDeliveryLog(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "delivery", "crawler.jsonl"))
	if err != nil {
		matches, _ := filepath.Glob(filepath.Join(dir, "delivery", "*"))
		t.Fatal(err, matches)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var e delivery.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		got = append(got, e.Cache+"/"+e.Reason+"/"+e.Check)
	}
	want := "HIT//verified BYPASS/unverified/unverified HIT//unverifiable"
	if strings.Join(got, " ") != want {
		t.Errorf("got %v, want %s", got, want)
	}
}

func TestReloadAppliesRulesAndReportsRestart(t *testing.T) {
	g, _, _ := verifiedGateway(t)
	next := g.Settings()
	next.Bots = append([]string{"ExampleBot"}, next.Bots...)
	next.AdminToken = "rotated"
	next.Listen = ":8181"
	next.RenderConcurrency = 2
	result, err := g.Apply(next)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Applied, ",") != "admin_token,bots" || strings.Join(result.RestartRequired, ",") != "listen,render_concurrency" {
		t.Fatalf("%+v", result)
	}
	if s := g.Settings(); s.Listen == ":8181" || s.RenderConcurrency == 2 || s.AdminToken != "rotated" || s.Bots[0] != "ExampleBot" {
		t.Errorf("settings after reload: %+v", s)
	}
	bad := g.Settings()
	bad.UnverifiableCrawlers = "maybe"
	if _, err := g.Apply(bad); err == nil {
		t.Error("an invalid configuration was applied")
	}
	if g.Settings().UnverifiableCrawlers != "snapshot" {
		t.Error("a refused reload changed settings")
	}

	// POST /reload on the admin listener, with the rotated token.
	called := false
	g.SetReloader(func(ctx context.Context) (Reload, error) {
		called = true
		return Reload{Applied: []string{"bots"}, RestartRequired: []string{}}, nil
	})
	admin := g.AdminHandler()
	r := httptest.NewRequest("POST", "http://admin.test/reload", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	admin.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || called {
		t.Fatalf("old token still accepted: %d", w.Code)
	}
	r.Header.Set("Authorization", "Bearer rotated")
	w = httptest.NewRecorder()
	admin.ServeHTTP(w, r)
	if w.Code != 200 || !called || !strings.Contains(w.Body.String(), `"applied":["bots"]`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	public := httptest.NewRecorder()
	g.ServeHTTP(public, httptest.NewRequest("POST", "http://public.test/reload", nil))
	if public.Header().Get("X-RenderCache") != "BYPASS" || public.Body.String() != "origin shell" {
		t.Error("reload reachable on the public listener")
	}
}

// A request keeps the settings it started with: a reload in the middle of
// it neither mixes old and new rules nor races.
func TestReloadDuringRequests(t *testing.T) {
	g, _, _ := verifiedGateway(t)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		on := false
		for {
			select {
			case <-stop:
				return
			default:
			}
			c := g.Settings()
			c.VerifyCrawlers = on
			on = !on
			if _, err := g.Apply(c); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := fetch(g, "GPTBot", "203.0.113.9:4000").Header().Get("X-RenderCache"); got != "HIT" {
			t.Fatalf("verified crawler got %s during reloads", got)
		}
	}
	close(stop)
	<-done
}
