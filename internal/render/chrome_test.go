package render

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rendercache/rendercache/internal/target"
)

func browserOptions(t *testing.T) Options {
	t.Helper()
	chrome := os.Getenv("RENDERCACHE_TEST_CHROME")
	if chrome == "" {
		t.Skip("set RENDERCACHE_TEST_CHROME for real-browser integration checks")
	}
	return Options{ChromePath: chrome, NoSandbox: os.Getenv("RENDERCACHE_TEST_NO_SANDBOX") == "1", Timeout: 10 * time.Second, Settle: 20 * time.Millisecond, ReadySelector: "#ready", MaxHTMLBytes: 1 << 20, Concurrency: 1}
}
func TestChromeRealRenderingAndNetworkPolicy(t *testing.T) {
	opts := browserOptions(t)
	var forbiddenCalls atomic.Int32
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forbiddenCalls.Add(1); io.WriteString(w, "secret") }))
	defer forbidden.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("X-Robots-Tag", "noarchive")
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/", 302)
			return
		case "/escape":
			http.Redirect(w, r, forbidden.URL, 302)
			return
		case "/cookie-redirect":
			w.Header().Set("Set-Cookie", "session=one")
			http.Redirect(w, r, "/", 302)
			return
		case "/no-store":
			w.Header().Set("Cache-Control", "no-store")
			io.WriteString(w, "<h1 id=ready>No store</h1>")
			return
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"page":"not HTML"}`)
			return
		case "/private":
			w.Header().Set("Cache-Control", "private")
			io.WriteString(w, "<h1 id=ready>Private</h1>")
			return
		case "/cookie":
			w.Header().Set("Set-Cookie", "session=one")
			io.WriteString(w, "<h1 id=ready>Cookie</h1>")
			return
		case "/missing":
			w.WriteHeader(404)
			io.WriteString(w, "missing")
			return
		}
		fmt.Fprintf(w, `<!doctype html><html><head><title>Shell</title><meta name="robots" content="index,follow"><link rel="canonical" href="https://public.example/"><script type="application/ld+json">{"@type":"WebPage"}</script></head><body><main></main><script>
  fetch(%q).catch(()=>{});const worker=new Worker(URL.createObjectURL(new Blob(['fetch(%q).catch(()=>{})'],{type:'text/javascript'})));
  setTimeout(()=>{document.querySelector('main').innerHTML='<h1 id="ready" onclick="alert(1)">Rendered content</h1>';document.title='Rendered title'},60);
  </script></body></html>`, forbidden.URL, forbidden.URL)
	}))
	defer origin.Close()
	engine, err := NewChrome(opts)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := target.Map("https://public.example", origin.URL, "/")
	result, err := engine.Render(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.HTML, "Rendered content") || strings.Contains(result.HTML, "setTimeout") || strings.Contains(result.HTML, "onclick=") || !strings.Contains(result.HTML, "application/ld+json") || result.Title != "Rendered title" || result.Canonical != "https://public.example/" || result.Robots != "index,follow" || result.Renderer == "" || result.FinalURL != p.URL || result.Headers["X-Robots-Tag"] != "noarchive" {
		t.Fatalf("bad capture: %+v", result)
	}
	if forbiddenCalls.Load() != 0 {
		t.Fatalf("blocked destination received %d requests", forbiddenCalls.Load())
	}
	if result.BlockedRequests == 0 {
		t.Fatal("no blocked requests recorded")
	}
	p, _ = target.Map("https://public.example", origin.URL, "/redirect")
	result, err = engine.Render(context.Background(), p)
	if err != nil || result.FinalURL != "https://public.example/" || len(result.Redirects) != 1 || result.Redirects[0].Status != 302 {
		t.Fatal(result, err)
	}
	for _, path := range []string{"/escape", "/private", "/cookie", "/cookie-redirect", "/no-store", "/json", "/missing"} {
		t.Run(path, func(t *testing.T) {
			p, _ := target.Map("https://public.example", origin.URL, path)
			if _, err := engine.Render(context.Background(), p); err == nil {
				t.Fatal("unsafe/failed page accepted")
			}
		})
	}
	if forbiddenCalls.Load() != 0 {
		t.Fatal("redirect escaped origin")
	}
}
func TestChromeTimeoutAndSize(t *testing.T) {
	opts := browserOptions(t)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<h1>Not ready</h1>")
	}))
	defer origin.Close()
	p, _ := target.Map(origin.URL, origin.URL, "/")
	opts.Timeout = 800 * time.Millisecond
	engine, _ := NewChrome(opts)
	start := time.Now()
	if _, err := engine.Render(context.Background(), p); err == nil {
		t.Fatal("missing selector did not time out")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("hard timeout exceeded")
	}
	opts.Timeout = 10 * time.Second
	opts.ReadySelector = ""
	opts.MaxHTMLBytes = 8
	engine, _ = NewChrome(opts)
	if _, err := engine.Render(context.Background(), p); err == nil {
		t.Fatal("oversized capture accepted")
	}
	opts.MaxHTMLBytes = 1 << 20
	opts.ChromePath = "/nonexistent/chrome"
	engine, _ = NewChrome(opts)
	if _, err := engine.Render(context.Background(), p); err == nil {
		t.Fatal("missing Chrome executable accepted")
	}
}

func TestChromeCapacityWaitHonorsDeadline(t *testing.T) {
	opts := browserOptions(t)
	opts.Timeout = 2 * time.Second
	entered := make(chan struct{}, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<h1>Never ready</h1>")
	}))
	defer origin.Close()
	p, _ := target.Map(origin.URL, origin.URL, "/")
	engine, _ := NewChrome(opts)
	first := make(chan error, 1)
	go func() { _, err := engine.Render(context.Background(), p); first <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first render never reached origin")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := engine.Render(ctx, p); err == nil {
		t.Fatal("capacity wait ignored cancellation")
	}
	if time.Since(started) > time.Second {
		t.Fatal("capacity wait did not respect request deadline")
	}
	if err := <-first; err == nil {
		t.Fatal("first render should time out")
	}
}
