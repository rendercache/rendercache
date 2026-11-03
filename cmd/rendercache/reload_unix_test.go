// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestServeReloadsConfigOnSIGHUP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app") }))
	defer upstream.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "rendercache.json")
	base := map[string]any{"origin": upstream.URL, "data_dir": filepath.Join(dir, "data"), "warm_enabled": false, "bots": []string{"GPTBot"}}
	writeConfig(t, path, base)
	s, logs := startServe(t, path)
	base["bots"] = []string{"ExampleBot"}
	writeConfig(t, path, base)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), `"msg":"reloaded"`) {
		if time.Now().After(deadline) {
			t.Fatal("no reload logged:", logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := cacheOutcome(t, s.Public, "ExampleBot/1.0"); got != "MISS" {
		t.Errorf("ExampleBot after SIGHUP: %s", got)
	}
	if got := cacheOutcome(t, s.Public, "GPTBot/1.2"); got != "BYPASS" {
		t.Errorf("GPTBot after SIGHUP: %s", got)
	}
}
