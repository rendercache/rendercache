package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthAuthAndNoRedirect(t *testing.T) {
	t.Setenv("RENDERCACHE_ADMIN_TOKEN", "health-secret")
	var redirected bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer destination.Close()
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer health-secret" {
			t.Error("missing health authorization")
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, destination.URL, 302)
			return
		}
		w.WriteHeader(200)
	}))
	defer admin.Close()
	if err := health(context.Background(), []string{"--admin-url", admin.URL + "/healthz"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := health(context.Background(), []string{"--admin-url", admin.URL + "/redirect"}, io.Discard); err == nil || redirected {
		t.Fatal("health followed redirect", err)
	}
}
