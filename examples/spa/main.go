// SPDX-License-Identifier: Apache-2.0
// A deliberately client-rendered public site for RenderCache integration checks.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8081", "fixture listen address")
	flag.Parse()
	handler, err := Handler()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("JavaScript fixture at http://" + *listen)
	log.Fatal(http.ListenAndServe(*listen, handler))
}
func Handler() (http.Handler, error) {
	html, err := os.ReadFile("examples/spa/index.html")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>http://%s/</loc></url><url><loc>http://%s/about</loc></url></urlset>`, r.Host, r.Host)
	})
	mux.HandleFunc("GET /api/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"title":"A page worth finding","text":"RenderCache makes this JavaScript-generated text available in a cached HTML snapshot."}`)
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/about" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(html)
	})
	return mux, nil
}
