package sitemap

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func options(origin string) Options {
	return Options{Site: "https://public.example", Origin: origin, URLs: []string{"/sitemap.xml"}, MaxBytes: 1 << 20, MaxURLs: 32, MaxDocuments: 8, MaxDepth: 4, Timeout: time.Second}
}
func TestIndexGzipMappingAndDedup(t *testing.T) {
	var externalCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("visitor credentials in sitemap request")
		}
		if r.Host == "public.example" {
			t.Error("public site fetched instead of origin")
		}
		switch r.URL.Path {
		case "/sitemap.xml":
			io.WriteString(w, `<?xml version="1.0"?><sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><sitemap><loc>https://public.example/pages.xml.gz</loc></sitemap><sitemap><loc>https://public.example/sitemap.xml</loc></sitemap></sitemapindex>`)
		case "/pages.xml.gz":
			w.Header().Set("Content-Type", "application/gzip")
			z := gzip.NewWriter(w)
			io.WriteString(z, `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>https://public.example/a%2Fb?x=2&amp;x=1</loc></url><url><loc>https://public.example/about</loc></url><url><loc>https://public.example/about#anchor</loc></url></urlset>`)
			z.Close()
		default:
			externalCalls.Add(1)
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()
	o := options(origin.URL)
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pages, err := s.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0].URL != "https://public.example/a%2Fb?x=2&x=1" || pages[0].Origin != origin.URL+"/a%2Fb?x=2&x=1" || externalCalls.Load() != 0 {
		t.Fatal(pages)
	}
}
func TestLimitsAndXMLRejection(t *testing.T) {
	cases := []struct {
		name, body string
		change     func(*Options)
	}{
		{"urls", `<urlset><url><loc>https://public.example/a</loc></url><url><loc>https://public.example/b</loc></url></urlset>`, func(o *Options) { o.MaxURLs = 1 }},
		{"bytes", strings.Repeat("x", 65), func(o *Options) { o.MaxBytes = 64 }},
		{"foreign page", `<urlset><url><loc>https://foreign.example/a</loc></url></urlset>`, nil},
		{"foreign index", `<sitemapindex><sitemap><loc>https://foreign.example/index.xml</loc></sitemap></sitemapindex>`, nil},
		{"duplicate loc", `<urlset><url><loc>https://public.example/a</loc><loc>https://public.example/b</loc></url></urlset>`, nil},
		{"relative loc", `<urlset><url><loc>/a</loc></url></urlset>`, nil},
		{"doctype", `<!DOCTYPE urlset [<!ENTITY x "test">]><urlset/>`, nil},
		{"truncated", `<urlset><url>`, nil}, {"extra root", `<urlset/><urlset/>`, nil}, {"wrong namespace", `<urlset xmlns="https://invalid.example"/>`, nil}, {"shell", `<html></html>`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.body) }))
			defer server.Close()
			o := options(server.URL)
			if tc.change != nil {
				tc.change(&o)
			}
			source, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			if pages, err := source.Discover(context.Background()); err == nil || pages != nil {
				t.Fatal("partial/invalid scan accepted", pages, err)
			}
		})
	}
}
func TestDocumentDepthAndGzipExpansionLimits(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sitemap.xml" {
			io.WriteString(w, `<sitemapindex><sitemap><loc>https://public.example/next.xml</loc></sitemap></sitemapindex>`)
			return
		}
		io.WriteString(w, `<sitemapindex><sitemap><loc>https://public.example/third.xml</loc></sitemap></sitemapindex>`)
	}))
	defer origin.Close()
	for _, mutate := range []func(*Options){func(o *Options) { o.MaxDocuments = 1 }, func(o *Options) { o.MaxDepth = 1 }} {
		o := options(origin.URL)
		mutate(&o)
		s, _ := New(o)
		if _, err := s.Discover(context.Background()); err == nil {
			t.Fatal("sitemap graph limit ignored")
		}
		s.Close()
	}
	compressed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		z := gzip.NewWriter(w)
		io.WriteString(z, strings.Repeat(" ", 8192)+"<urlset/>")
		z.Close()
	}))
	defer compressed.Close()
	o := options(compressed.URL)
	o.MaxBytes = 1024
	s, _ := New(o)
	defer s.Close()
	if _, err := s.Discover(context.Background()); err == nil {
		t.Fatal("gzip expansion limit ignored")
	}
}
func TestRedirectPolicyAndCancellation(t *testing.T) {
	var external atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { external.Add(1) }))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "/empty", 302)
		case "/empty":
			io.WriteString(w, "<urlset/>")
		case "/slow":
			<-r.Context().Done()
		default:
			http.Redirect(w, r, foreign.URL, 302)
		}
	}))
	defer origin.Close()
	o := options(origin.URL)
	s, _ := New(o)
	if _, err := s.Discover(context.Background()); err == nil || external.Load() != 0 {
		t.Fatal("external redirect followed")
	}
	s.Close()
	o.URLs = []string{"/same"}
	s, _ = New(o)
	pages, err := s.Discover(context.Background())
	s.Close()
	if err != nil || len(pages) != 0 {
		t.Fatal(pages, err)
	}
	o.URLs = []string{"/slow"}
	o.ScanTimeout = 30 * time.Millisecond
	s, _ = New(o)
	defer s.Close()
	started := time.Now()
	if _, err := s.Discover(context.Background()); err == nil {
		t.Fatal("scan deadline ignored")
	}
	if time.Since(started) > time.Second {
		t.Fatal("scan did not cancel")
	}
}
func TestGzipHTTPEncodingAndDuplicateCycle(t *testing.T) {
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	fmt.Fprint(z, "<urlset/>")
	z.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(compressed.Bytes())
	}))
	defer server.Close()
	s, _ := New(options(server.URL))
	defer s.Close()
	if pages, err := s.Discover(context.Background()); err != nil || len(pages) != 0 {
		t.Fatal(pages, err)
	}
	cycle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<sitemapindex><sitemap><loc>https://public.example/sitemap.xml</loc></sitemap></sitemapindex>`)
	}))
	defer cycle.Close()
	o := options(cycle.URL)
	o.MaxDepth = 0
	loop, _ := New(o)
	defer loop.Close()
	if _, err := loop.Discover(context.Background()); err != nil {
		t.Fatal("visited cycle should not consume depth", err)
	}
}
