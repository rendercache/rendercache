package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rendercache/rendercache/internal/config"
	"github.com/rendercache/rendercache/internal/snapshot"
	"github.com/rendercache/rendercache/internal/target"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }
func TestCLI(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"serve", "--help"}, {"version"}} {
		if err := run(context.Background(), args, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"unknown"}, {"serve"}, {"serve", "--origin", "ftp://bad"}, {"serve", "--origin", "https://example.com", "extra"}} {
		if run(context.Background(), args, io.Discard, io.Discard) == nil {
			t.Fatal("accepted", args)
		}
	}
}
func TestRunAndGracefulShutdown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "fixture shell") }))
	defer upstream.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--origin", upstream.URL, "--listen", "127.0.0.1:0", "--admin-listen", "127.0.0.1:0"}, io.Discard, &logs)
	}()
	var entry struct {
		Public string `json:"public"`
		Admin  string `json:"admin"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(logs.String(), "\n") {
			_ = json.Unmarshal([]byte(line), &entry)
		}
		if entry.Public != "" {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("early exit: %v", err)
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	if entry.Public == "" {
		t.Fatal("no listening event", logs.String())
	}
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + entry.Public + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "fixture shell" {
		t.Fatal(string(body))
	}
	resp, err = client.Get("http://" + entry.Admin + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not finish")
	}
}

func TestRenderInspectCommands(t *testing.T) {
	chrome := os.Getenv("RENDERCACHE_TEST_CHROME")
	if chrome == "" {
		t.Skip("requires Chrome integration environment")
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<main></main><script>document.querySelector('main').innerHTML='<h1 id="ready">CLI rendered</h1>'</script>`)
	}))
	defer origin.Close()
	dir := t.TempDir()
	args := []string{"--origin", origin.URL, "--public-url", "https://public.example", "--url", "/", "--data-dir", dir}
	renderArgs := append([]string{"render"}, args...)
	renderArgs = append(renderArgs, "--chrome-path", chrome, "--ready-selector", "#ready")
	if os.Getenv("RENDERCACHE_TEST_NO_SANDBOX") == "1" {
		renderArgs = append(renderArgs, "--chrome-no-sandbox")
	}
	var out bytes.Buffer
	if err := run(context.Background(), renderArgs, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, receipt.Key+".json")); err != nil {
		t.Fatal(err)
	}
	// Separate command invocation loads disk data without a Chrome dependency.
	out.Reset()
	if err := run(context.Background(), append([]string{"inspect"}, args...), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var record struct {
		Result struct {
			HTML string `json:"html"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(record.Result.HTML, "CLI rendered") || strings.Contains(record.Result.HTML, "<script>") {
		t.Fatal(record.Result.HTML)
	}
}

func TestServeWarmsBeforeBotAndReusesDisk(t *testing.T) {
	chrome := os.Getenv("RENDERCACHE_TEST_CHROME")
	if chrome == "" {
		t.Skip("requires Chrome integration environment")
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sitemap.xml" {
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, `<urlset><url><loc>https://public.example/one</loc></url><url><loc>https://public.example/two</loc></url></urlset>`)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<title>Shell</title><main></main><script>document.querySelector('main').innerHTML='<h1 id="ready">Prewarmed page</h1>'</script>`)
	}))
	defer origin.Close()
	cfg := config.Default()
	cfg.Origin = origin.URL
	cfg.PublicURL = "https://public.example"
	cfg.DataDir = t.TempDir()
	cfg.ChromePath = chrome
	cfg.ChromeNoSandbox = os.Getenv("RENDERCACHE_TEST_NO_SANDBOX") == "1"
	cfg.ReadySelector = "#ready"
	cfg.RenderTimeout = "10s"
	configPath := filepath.Join(t.TempDir(), "config.json")
	writeConfig := func() {
		b, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(configPath, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig()
	type state struct {
		Rendering bool `json:"rendering_enabled"`
		Serving   bool `json:"snapshot_serving_enabled"`
		Requests  int  `json:"requests"`
		Warming   struct {
			State      string `json:"state"`
			Discovered int    `json:"discovered"`
			Cached     int    `json:"cached"`
			Successes  int    `json:"render_successes"`
		} `json:"warming"`
	}
	client := &http.Client{Timeout: time.Second}
	start := func() (string, string, func()) {
		ctx, cancel := context.WithCancel(context.Background())
		logs := &lockedBuffer{}
		done := make(chan error, 1)
		go func() {
			done <- run(ctx, []string{"serve", "--config", configPath, "--listen", "127.0.0.1:0", "--admin-listen", "127.0.0.1:0"}, io.Discard, logs)
		}()
		stop := func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(4 * time.Second):
				t.Error("warming did not stop with service")
			}
		}
		var entry struct {
			Public string `json:"public"`
			Admin  string `json:"admin"`
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, line := range strings.Split(logs.String(), "\n") {
				_ = json.Unmarshal([]byte(line), &entry)
			}
			if entry.Public != "" {
				return "http://" + entry.Public, "http://" + entry.Admin, stop
			}
			time.Sleep(time.Millisecond)
		}
		stop()
		t.Fatal("no listening event", logs.String())
		return "", "", func() {}
	}
	getStatus := func(admin string) state {
		resp, err := client.Get(admin + "/status")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var s state
		if err = json.NewDecoder(resp.Body).Decode(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	public, admin, stop := start()
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()
	deadline := time.Now().Add(12 * time.Second)
	var s state
	for time.Now().Before(deadline) {
		s = getStatus(admin)
		if s.Warming.State == "ready" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.Warming.Cached != 2 || s.Warming.Discovered != 2 || s.Warming.Successes != 2 || s.Requests != 0 || !s.Rendering || s.Serving {
		t.Fatalf("warming did not prepare pages before bot: %+v", s)
	}
	store, _ := snapshot.Open(cfg.DataDir, cfg.MaxHTMLBytes)
	for _, path := range []string{"/one", "/two"} {
		p, _ := target.Map(cfg.SiteURL(), cfg.Origin, path)
		record, err := store.Load(p)
		if err != nil || !strings.Contains(record.Result.HTML, "<h1 id=\"ready\">Prewarmed page</h1>") {
			t.Fatal(record, err)
		}
	}
	req, _ := http.NewRequest("GET", public+"/one", nil)
	req.Header.Set("User-Agent", "GPTBot")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-RenderCache") != "MISS" {
		t.Fatal("session 3 should still proxy bots")
	}
	stop()
	stopped = true
	cfg.ChromePath = "/missing/chrome"
	writeConfig()
	_, admin, stop = start()
	defer stop()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s = getStatus(admin)
		if s.Warming.State == "ready" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.Warming.Cached != 2 || s.Warming.Successes != 0 {
		t.Fatal(fmt.Sprintf("fresh disk snapshots were not reused: %+v", s))
	}
}
