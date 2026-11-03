// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type served struct {
	Public string `json:"public"`
	Admin  string `json:"admin"`
}

// startServe runs serve with the given config file until the test ends.
func startServe(t *testing.T, configPath string) (served, *lockedBuffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	logs := &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--config", configPath, "--listen", "127.0.0.1:0", "--admin-listen", "127.0.0.1:0"}, io.Discard, logs)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("shutdown did not finish")
		}
	})
	var entry served
	deadline := time.Now().Add(5 * time.Second)
	for entry.Public == "" && time.Now().Before(deadline) {
		for _, line := range strings.Split(logs.String(), "\n") {
			_ = json.Unmarshal([]byte(line), &entry)
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
	return entry, logs
}

func writeConfig(t *testing.T, path string, values map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(values)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func cacheOutcome(t *testing.T, address, ua string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://"+address+"/page", nil)
	req.Header.Set("User-Agent", ua)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.Header.Get("X-RenderCache")
}

func reloadConfig(t *testing.T, admin string) (int, map[string]any) {
	t.Helper()
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Post("http://"+admin+"/reload", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestServeReloadsConfigOnAdminPost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app") }))
	defer upstream.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "rendercache.json")
	base := map[string]any{"origin": upstream.URL, "data_dir": filepath.Join(dir, "data"), "warm_enabled": false, "bots": []string{"GPTBot"}}
	writeConfig(t, path, base)
	s, _ := startServe(t, path)
	if got := cacheOutcome(t, s.Public, "ExampleBot/1.0"); got != "BYPASS" {
		t.Fatalf("before reload: %s", got)
	}
	base["bots"] = []string{"GPTBot", "ExampleBot"}
	base["render_concurrency"] = 2
	writeConfig(t, path, base)
	code, out := reloadConfig(t, s.Admin)
	if code != 200 || !strings.Contains(asJSON(out["applied"]), "bots") || !strings.Contains(asJSON(out["restart_required"]), "render_concurrency") {
		t.Fatalf("%d %v", code, out)
	}
	if got := cacheOutcome(t, s.Public, "ExampleBot/1.0"); got != "MISS" {
		t.Fatalf("after reload: %s", got)
	}
	// A broken file is refused and the running settings stay.
	os.WriteFile(path, []byte(`{"origin": "`), 0o600)
	if code, out := reloadConfig(t, s.Admin); code != http.StatusUnprocessableEntity || out["error"] == "" {
		t.Fatalf("broken config: %d %v", code, out)
	}
	if got := cacheOutcome(t, s.Public, "ExampleBot/1.0"); got != "MISS" {
		t.Fatalf("after refused reload: %s", got)
	}
}

func asJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }
