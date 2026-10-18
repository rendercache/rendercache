package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rendercache/rendercache/internal/config"
	"github.com/rendercache/rendercache/internal/delivery"
	"github.com/rendercache/rendercache/internal/render"
	"github.com/rendercache/rendercache/internal/snapshot"
	"github.com/rendercache/rendercache/internal/target"
)

func demoGateway(t *testing.T, origin http.Handler) (*Gateway, *delivery.Log) {
	t.Helper()
	up := httptest.NewServer(origin)
	t.Cleanup(up.Close)
	c := config.Default()
	c.Origin = up.URL
	c.PublicURL = "https://public.example"
	c.AdminToken = "demo-secret"
	c.DataDir = t.TempDir()
	g, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	store, err := snapshot.Open(c.DataDir, c.MaxHTMLBytes)
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	}) // Windows cannot delete a folder still held open
	if err != nil {
		t.Fatal(err)
	}
	g.SetSnapshotStore(store)
	l, err := delivery.Open(t.TempDir(), 4096, 2)
	if err != nil {
		t.Fatal(err)
	}
	// Close the log before its temp folder is removed (cleanups run last-in first-out).
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		l.Close(ctx)
	})
	g.SetDeliveryLog(l)
	return g, l
}
func adminJSON(g *Gateway, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://admin.test"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+g.cfg.AdminToken)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.AdminHandler().ServeHTTP(w, r)
	return w
}
func TestDemoPrivacyMappingLimitsAndAuth(t *testing.T) {
	var mode atomic.Int32
	var calls atomic.Int32
	g, _ := demoGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("visitor credentials reached raw fetch")
		}
		w.Header().Set("Content-Type", "text/html")
		switch mode.Load() {
		case 1:
			w.Header().Set("Set-Cookie", "private=x")
		case 2:
			w.Header().Set("Cache-Control", "no-store")
		case 3:
			http.Redirect(w, r, "https://external.example", 302)
			return
		case 4:
			io.WriteString(w, strings.Repeat("x", (1<<20)+1))
			return
		case 5:
			w.Header().Set("Vary", "Cookie")
		}
		io.WriteString(w, `<main></main><script>window.evil=true</script><noscript>Enable JS</noscript>`)
	}))
	result := render.Result{Status: 200, HTML: `<h1>Public content with ten or more useful words that a crawler should see</h1><p>&lt;script&gt;escaped attack&lt;/script&gt;</p>`}
	if _, err := g.store.Save(mapped(g, "/"), result); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/ui.js", "/ui.css", "/status"} {
		r := httptest.NewRequest("GET", "http://admin.test"+path, nil)
		w := httptest.NewRecorder()
		g.AdminHandler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(path, w.Code)
		}
		r.SetBasicAuth("rendercache", "demo-secret")
		w = httptest.NewRecorder()
		g.AdminHandler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(path, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "http://admin.test/status", nil)
	r.Header.Set("Authorization", "demo-secret")
	w := httptest.NewRecorder()
	g.AdminHandler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("accepted token without scheme")
	}
	w = adminJSON(g, "/diagnose", `{"url":"/"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"likely_shell":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	before := calls.Load()
	for _, body := range []string{`{"url":"https://external.example/"}`, `{"url":"/api/secret"}`, `{"url":"/image.png"}`, `{"url":"/","extra":true}`, `{"url":"/"}{}`} {
		w = adminJSON(g, "/diagnose", body)
		if w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
	if calls.Load() != before {
		t.Fatal("invalid URL was fetched")
	}
	r = httptest.NewRequest("POST", "http://admin.test/diagnose", strings.NewReader(`{"url":"/"}`))
	r.Header.Set("Authorization", "Bearer demo-secret")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.test")
	w = httptest.NewRecorder()
	g.AdminHandler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	for _, modeValue := range []int32{1, 2, 3, 4, 5} {
		mode.Store(modeValue)
		w = adminJSON(g, "/diagnose", `{"url":"/"}`)
		if w.Code == 200 || strings.Contains(w.Body.String(), "window.evil") {
			t.Fatal(modeValue, w.Code, w.Body.String())
		}
		if _, err := g.store.Load(mapped(g, "/")); err != nil {
			t.Fatal("diagnostic changed saved snapshot", err)
		}
	}
}

type failingResponse struct{ h http.Header }

func (w *failingResponse) Header() http.Header         { return w.h }
func (w *failingResponse) WriteHeader(int)             {}
func (w *failingResponse) Write(b []byte) (int, error) { return 2, io.ErrClosedPipe }
func TestDeliveryActualBytesBypassesAndSimulation(t *testing.T) {
	g, l := demoGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "origin") }))
	record, err := g.store.Save(mapped(g, "/"), render.Result{Status: 200, HTML: "<h1>Saved HTML</h1>"})
	if err != nil {
		t.Fatal(err)
	}
	bot(g, "GET", "/?token=do-not-log", nil) // Deliberate MISS: query is a distinct key.
	bot(g, "GET", "/", nil)
	bot(g, "HEAD", "/", nil)
	bot(g, "GET", "/", map[string]string{"If-None-Match": record.ETag()})
	bot(g, "GET", "/", map[string]string{"Cookie": "private=secret"})
	bot(g, "GET", "/api/data", nil)
	for path, result := range map[string]render.Result{"/gone": {Status: 410}, "/redirect": {Status: 302, Location: "https://public.example/"}} {
		if _, err := g.store.Save(mapped(g, path), result); err != nil {
			t.Fatal(err)
		}
		bot(g, "GET", path, nil)
	}
	r := httptest.NewRequest("GET", "http://public.example/", nil)
	r.Header.Set("User-Agent", "GPTBot")
	g.ServeHTTP(&failingResponse{h: make(http.Header)}, r)
	w := adminJSON(g, "/simulate", `{"url":"/"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	events := l.Status().Recent
	if len(events) != 10 {
		t.Fatalf("%+v", events)
	}
	if events[0].Path != "/" || events[0].Cache != "MISS" || events[0].FullHTML || events[0].Bytes != 6 {
		t.Fatal(events[0])
	}
	if !events[1].FullHTML || !events[1].Cached || events[1].Bytes != int64(len(record.Result.HTML)) {
		t.Fatal(events[1])
	}
	for _, i := range []int{2, 3, 4, 5, 6, 7, 8} {
		if events[i].FullHTML {
			t.Fatal(i, events[i])
		}
	}
	if events[2].Bytes != 0 || events[3].Status != 304 || events[4].Cache != "BYPASS" || events[5].Cache != "BYPASS" || !events[8].WriteFailed || events[8].Bytes != 2 || !events[9].Simulated || !events[9].FullHTML {
		t.Fatal(events)
	}
}
func TestExtraDiagnosticsExposeRequestFailures(t *testing.T) {
	g, store, _, _ := cachedGateway(t)
	m := attach(t, g, store, engineFunc(func(context.Context, target.Page) (render.Result, error) {
		return render.Result{}, errors.New("renderer unavailable")
	}), 2)
	_, err := m.Request(mapped(g, "/extra"), "cold")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return m.Status().ExtraFailed == 1 })
	s := m.Status()
	if len(s.ExtraDetails) != 1 || s.ExtraDetails[0].LastError != "renderer unavailable" || s.ExtraDetails[0].Failures < 1 {
		t.Fatal(s)
	}
}

func TestCrawlerProtocolUpgradeReceipt(t *testing.T) {
	g, l := demoGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: example\r\n\r\ntunnel-data")
		_ = buf.Flush()
	}))
	public := httptest.NewServer(g)
	defer public.Close()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(public.URL, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: public.example\r\nUser-Agent: GPTBot\r\nConnection: Upgrade\r\nUpgrade: example\r\n\r\n")
	response, err := io.ReadAll(conn)
	if err != nil || !strings.Contains(string(response), "101 Switching Protocols") || !strings.Contains(string(response), "tunnel-data") {
		t.Fatal(string(response), err)
	}
	_ = conn.Close()
	waitFor(t, func() bool { return len(l.Status().Recent) == 1 })
	e := l.Status().Recent[0]
	if e.Status != 101 || !e.Upgraded || e.Cache != "BYPASS" || e.FullHTML || e.Bytes != 0 {
		t.Fatal(e)
	}
}
func TestInterruptedDeliveryIsMarkedFailed(t *testing.T) {
	g, l := demoGateway(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	r := httptest.NewRequest("GET", "http://public.example/", nil)
	r.Header.Set("User-Agent", "GPTBot")
	func() {
		defer func() {
			if recover() != http.ErrAbortHandler {
				t.Error("abort was swallowed")
			}
		}()
		_, finish := g.observe(httptest.NewRecorder(), r, Route{Outcome: "MISS"})
		defer finish()
		panic(http.ErrAbortHandler)
	}()
	e := l.Status().Recent[0]
	if !e.WriteFailed || e.Status != 0 || e.FullHTML {
		t.Fatal(e)
	}
}
