package main

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestFixture(t *testing.T) {
	// go test executes in the package directory; Handler's production path is repo-root-relative.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir("../.."); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(dir)
	h, err := Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, contains string
		status         int
	}{{"/", `<main id="app"></main>`, 200}, {"/about", `fetch('/api/page')`, 200}, {"/api/page", `A page worth finding`, 200}, {"/sitemap.xml", `http://fixture.test/about`, 200}, {"/missing", "404", 404}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://fixture.test"+tc.path, nil))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
}
