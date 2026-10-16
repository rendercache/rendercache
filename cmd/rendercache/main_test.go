package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
