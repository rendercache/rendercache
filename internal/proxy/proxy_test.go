package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rendercache/rendercache/internal/config"
)

func TestClassify(t *testing.T) {
	tests := []struct{ name, method, path, accept, ua, header, value, outcome, reason string }{
		{"ai bot", "GET", "/story", "text/html", "GPTBot", "", "", "MISS", "snapshot-unavailable"},
		{"case insensitive", "HEAD", "/story?x=2", "*/*", "gptbot/1.0", "", "", "MISS", "snapshot-unavailable"},
		{"human", "GET", "/", "text/html", "Mozilla/5.0", "", "", "BYPASS", "human"},
		{"private cookie", "GET", "/", "text/html", "GPTBot", "Cookie", "session=secret", "BYPASS", "private"},
		{"authorization", "GET", "/", "text/html", "GPTBot", "Authorization", "Bearer secret", "BYPASS", "private"},
		{"write", "POST", "/", "text/html", "GPTBot", "", "", "BYPASS", "method"},
		{"asset", "GET", "/app.js", "*/*", "GPTBot", "", "", "BYPASS", "asset"},
		{"sitemap", "GET", "/sitemap.xml", "*/*", "GPTBot", "", "", "BYPASS", "asset"},
		{"api", "GET", "/api/page", "*/*", "GPTBot", "", "", "BYPASS", "api"},
		{"json", "GET", "/page", "application/json", "GPTBot", "", "", "BYPASS", "accept"},
		{"no html", "GET", "/page", "text/html;q=0, application/json", "GPTBot", "", "", "BYPASS", "accept"},
		{"range", "GET", "/", "text/html", "GPTBot", "Range", "bytes=0-10", "BYPASS", "protocol"},
		{"upgrade", "GET", "/", "*/*", "GPTBot", "Upgrade", "websocket", "BYPASS", "protocol"},
		{"no store", "GET", "/", "text/html", "GPTBot", "Cache-Control", "no-store", "BYPASS", "no-store"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "http://public.test"+tt.path, nil)
			r.Header.Set("Accept", tt.accept)
			r.Header.Set("User-Agent", tt.ua)
			if tt.header != "" {
				r.Header.Set(tt.header, tt.value)
			}
			got := Classify(r, []string{"GPTBot"})
			if got.Outcome != tt.outcome || got.Reason != tt.reason {
				t.Fatalf("got %+v", got)
			}
		})
	}
	r := httptest.NewRequest("GET", "http://public.test/", nil)
	r.Header.Set("User-Agent", "CustomBot")
	if Classify(r, []string{"CustomBot"}).Outcome != "MISS" {
		t.Fatal("custom bot ignored")
	}
	if Classify(r, nil).Outcome != "BYPASS" {
		t.Fatal("empty bot list ignored")
	}
}
func gateway(t *testing.T, origin string) *Gateway {
	t.Helper()
	c := config.Default()
	c.Origin = origin
	g, err := New(c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g
}
func TestProxyOriginAndIsolation(t *testing.T) {
	calls := make(chan *http.Request, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls <- r.Clone(context.Background())
		w.Header().Set("X-RenderCache", "forged")
		w.Header().Set("Vary", "Accept-Encoding")
		w.Header().Set("Set-Cookie", "origin=kept")
		w.WriteHeader(201)
		io.WriteString(w, "origin body")
	}))
	defer upstream.Close()
	g := gateway(t, upstream.URL)
	for _, ua := range []string{"Mozilla/5.0", "GPTBot"} {
		r := httptest.NewRequest("GET", "http://public.test/story?a=1&a=2", nil)
		r.Header.Set("User-Agent", ua)
		r.Header.Set("X-Forwarded-For", "spoofed")
		r.Header.Set("X-Forwarded-Host", "spoofed")
		r.Header.Set("X-RenderCache", "HIT")
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 201 || w.Body.String() != "origin body" {
			t.Fatalf("response: %d %s", w.Code, w.Body.String())
		}
		want := "BYPASS"
		if ua == "GPTBot" {
			want = "MISS"
		}
		if w.Header().Get("X-RenderCache") != want {
			t.Fatal(w.Header())
		}
		if !strings.Contains(strings.Join(w.Header().Values("Vary"), ","), "User-Agent") || w.Header().Get("Set-Cookie") != "origin=kept" {
			t.Fatal(w.Header())
		}
		got := <-calls
		if got.URL.RequestURI() != "/story?a=1&a=2" || got.Host == "public.test" {
			t.Fatalf("wrong upstream: %s %s", got.Host, got.URL)
		}
		if strings.Contains(got.Header.Get("X-Forwarded-For"), "spoofed") || got.Header.Get("X-Forwarded-Host") != "public.test" || got.Header.Get("X-RenderCache") != "" {
			t.Fatal(got.Header)
		}
	}
	// /status belongs to the origin on the public listener, never to admin.
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("GET", "http://public.test/status", nil))
	if w.Code != 201 {
		t.Fatal("public admin route leaked")
	}
	<-calls
}
func TestOriginFailureAndAdmin(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	upstream.Close()
	g := gateway(t, upstream.URL)
	r := httptest.NewRequest("GET", "http://public.test/", nil)
	r.Header.Set("User-Agent", "GPTBot")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 502 || w.Header().Get("X-RenderCache") != "MISS" {
		t.Fatal(w.Code, w.Header())
	}
	g.cfg.AdminToken = "secret"
	for _, token := range []string{"", "Bearer wrong", "Bearer secret"} {
		r := httptest.NewRequest("GET", "http://admin/status", nil)
		r.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		g.AdminHandler().ServeHTTP(w, r)
		want := 401
		if token == "Bearer secret" {
			want = 200
			if !strings.Contains(w.Body.String(), `"snapshot_serving_enabled":false`) {
				t.Fatal(w.Body.String())
			}
		}
		if w.Code != want {
			t.Fatal(w.Code)
		}
	}
}
func TestUpstreamHeaderTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer upstream.Close()
	c := config.Default()
	c.Origin = upstream.URL
	c.OriginTimeout = "20ms"
	g, err := New(c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("GET", "http://public.test/", nil))
	if w.Code != 502 {
		t.Fatal(w.Code)
	}
}

func TestDocumentAcceptQuality(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"text/html;q=0.00", false}, {"text/html;q=0.5", true}, {"application/json, text/html;q=0", false},
		{"text/html;q=invalid", false}, {"text/html;q=1.5", false}, {"text/*", true},
	} {
		if got := acceptsHTML(tc.value); got != tc.want {
			t.Errorf("%q: got %v", tc.value, got)
		}
	}
}
func TestProxyPreservesPrivateWrite(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || string(body) != "payload" || r.Header.Get("Authorization") != "Bearer example" || r.Header.Get("Cookie") != "session=example" {
			t.Error("private request changed")
		}
		w.Header().Set("Location", "/saved")
		w.WriteHeader(303)
	}))
	defer upstream.Close()
	g := gateway(t, upstream.URL)
	r := httptest.NewRequest("POST", "http://public.test/save", strings.NewReader("payload"))
	r.Header.Set("Authorization", "Bearer example")
	r.Header.Set("Cookie", "session=example")
	r.Header.Set("User-Agent", "GPTBot")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 303 || w.Header().Get("Location") != "/saved" || w.Header().Get("X-RenderCache") != "BYPASS" {
		t.Fatal(w.Code, w.Header())
	}
}
