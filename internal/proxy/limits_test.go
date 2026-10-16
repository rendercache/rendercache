package proxy

import (
	"github.com/rendercache/rendercache/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPAdmissionBodyAndTotalOriginDeadline(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			entered <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, "ok")
	}))
	defer up.Close()
	c := config.Default()
	c.Origin = up.URL
	c.HTTPConcurrency = 1
	c.MaxRequestBytes = 4
	c.OriginTimeout = "100ms"
	g, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://public.test/slow", nil))
	}()
	<-entered
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("GET", "http://public.test/", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("origin request exceeded total deadline")
	}
	w = httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest("POST", "http://public.test/", strings.NewReader("oversized")))
	if w.Code != 413 {
		t.Fatal(w.Code)
	}
}

func TestEdgeKeyIsRequiredAndNeverForwarded(t *testing.T) {
	var leaked atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("X-RenderCache-Edge-Key") != "")
		io.WriteString(w, "origin")
	}))
	defer up.Close()
	c := config.Default()
	c.Origin = up.URL
	c.EdgeToken = "edge-secret"
	g, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	r := httptest.NewRequest("GET", "http://public.example/", nil)
	r.Header.Set("User-Agent", "GPTBot")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("GET", "http://public.example/", nil)
	r.Header.Set("X-RenderCache-Edge-Key", "edge-secret")
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 200 || leaked.Load() {
		t.Fatal(w.Code, leaked.Load())
	}
}
