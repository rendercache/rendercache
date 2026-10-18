// SPDX-License-Identifier: Apache-2.0
package proxy

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rendercache/rendercache/internal/delivery"
	"github.com/rendercache/rendercache/internal/detect"
	"github.com/rendercache/rendercache/internal/render"
	"github.com/rendercache/rendercache/internal/target"
)

//go:embed ui/*
var ui embed.FS

func (g *Gateway) authorized(r *http.Request) bool {
	candidate := ""
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		candidate = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if user, password, ok := r.BasicAuth(); ok && user == "rendercache" {
		candidate = password
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(g.cfg.AdminToken)) == 1
}
func (g *Gateway) deliveryStatus() delivery.Status {
	if g.deliveries == nil {
		return delivery.Status{Recent: []delivery.Event{}}
	}
	return g.deliveries.Status()
}
func (g *Gateway) demo(w http.ResponseWriter, r *http.Request) {
	name := "index.html"
	if r.URL.Path == "/ui.js" {
		name = "app.js"
	}
	if r.URL.Path == "/ui.css" {
		name = "style.css"
	}
	body, _ := ui.ReadFile("ui/" + name)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	content := "text/html; charset=utf-8"
	if name == "app.js" {
		content = "text/javascript; charset=utf-8"
	}
	if name == "style.css" {
		content = "text/css; charset=utf-8"
	}
	w.Header().Set("Content-Type", content)
	_, _ = w.Write(body)
}
func failure(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
func (g *Gateway) demoPage(w http.ResponseWriter, r *http.Request) (target.Page, bool) {
	// JSON-only plus same-origin checks prevent cross-site form submissions on a
	// loopback admin listener without a token. Never accept visitor credentials.
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			failure(w, 403, "same-origin request required")
			return target.Page{}, false
		}
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		failure(w, 415, "application/json required")
		return target.Page{}, false
	}
	var input struct {
		URL string `json:"url"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		failure(w, 400, "one URL is required")
		return target.Page{}, false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || input.URL == "" {
		failure(w, 400, "one URL is required")
		return target.Page{}, false
	}
	p, err := g.mapPage(input.URL)
	if err != nil {
		failure(w, 400, "URL is outside the configured public site")
		return target.Page{}, false
	}
	check, _ := http.NewRequest("GET", p.URL, nil)
	check.Header.Set("User-Agent", "RenderCache-Demo")
	if ClassifyPaths(check, []string{"RenderCache-Demo"}, g.cfg.BypassPaths).Outcome != "MISS" {
		failure(w, 400, "URL is not an eligible HTML page")
		return target.Page{}, false
	}
	return p, true
}
func (g *Gateway) diagnose(w http.ResponseWriter, r *http.Request) {
	p, ok := g.demoPage(w, r)
	if !ok {
		return
	}
	select {
	case g.diagnostics <- struct{}{}:
		defer func() { <-g.diagnostics }()
	default:
		failure(w, 429, "diagnostics busy")
		return
	}
	if g.store == nil {
		failure(w, 503, "snapshot serving is disabled")
		return
	}
	if g.warming != nil && g.warming.Suppressed(p) {
		failure(w, 409, "snapshot withdrawn for privacy")
		return
	}
	// No cookie jar, authorization or inherited request headers, and no redirects.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", p.Origin, nil)
	req.Header.Set("User-Agent", "RenderCache-Diagnostic/1.0")
	req.Header.Set("Accept", "text/html")
	client := &http.Client{Transport: g.transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		failure(w, 502, "raw origin fetch failed")
		return
	}
	defer resp.Body.Close()
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	headers := map[string]string{}
	for k, values := range resp.Header {
		headers[k] = strings.Join(values, ",")
	}
	if resp.StatusCode != 200 || media != "text/html" || render.ValidateOutcome(p, render.Result{Status: 200, HTML: "public", Headers: headers}) != nil {
		failure(w, 409, "raw response is not public HTML; body withheld")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		failure(w, 502, "raw HTML exceeds diagnostic limit or could not be read")
		return
	}
	record, err := g.store.Load(p)
	if err != nil {
		failure(w, 404, "no saved snapshot; warm this URL first")
		return
	}
	if g.warming != nil && g.warming.Suppressed(p) {
		failure(w, 409, "snapshot withdrawn for privacy")
		return
	}
	if record.Result.Status != 200 {
		failure(w, 409, "saved response is a redirect or removal, not HTML")
		return
	}
	if len(record.Result.HTML) > 1<<20 {
		failure(w, 413, "snapshot exceeds 1 MiB diagnostic limit")
		return
	}
	comparison := detect.Compare(string(raw), record.Result.HTML)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"comparison": comparison, "raw_html": string(raw), "snapshot_html": record.Result.HTML, "snapshot_at": record.CreatedAt, "heuristic": "Likely shell: at least 10 rendered words, at least 10 missing words, and raw words at most 20% of rendered words. CSS/layout are not evaluated; this is a diagnostic, not an SEO score."})
}

// A simulated request traverses the same public handler and response accounting.
// It discards the body and cannot inject credentials or an arbitrary user agent.
func (g *Gateway) simulate(w http.ResponseWriter, r *http.Request) {
	p, ok := g.demoPage(w, r)
	if !ok {
		return
	}
	if len(g.cfg.Bots) == 0 || g.deliveries == nil {
		failure(w, 503, "crawler rules and delivery logging must be enabled")
		return
	}
	select {
	case g.diagnostics <- struct{}{}:
		defer func() { <-g.diagnostics }()
	default:
		failure(w, 429, "diagnostics busy")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), simulatedKey{}, true), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", p.URL, nil)
	request.Header.Set("User-Agent", g.cfg.Bots[0])
	request.Header.Set("X-RenderCache-Edge-Key", g.cfg.EdgeToken)
	request.Header.Set("Accept", "text/html")
	sink := &discardResponse{header: make(http.Header)}
	// ReverseProxy may abort a streamed response with ErrAbortHandler. Preserve
	// the receipt, but don't abort the admin connection on simulated upstream loss.
	aborted := false
	func() {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					aborted = true
				} else {
					panic(v)
				}
			}
		}()
		g.ServeHTTP(sink, request)
	}()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"simulated": true, "crawler_claim": g.cfg.Bots[0], "status": sink.status, "cache": sink.header.Get("X-RenderCache"), "delivered_bytes": sink.bytes, "aborted": aborted, "meaning": "Local simulation only; no external crawler visited. Delivery records do not prove indexing or citation."})
}

type discardResponse struct {
	header http.Header
	status int
	bytes  int64
}

func (w *discardResponse) Header() http.Header { return w.header }
func (w *discardResponse) WriteHeader(s int) {
	if w.status == 0 {
		w.status = s
	}
}
func (w *discardResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	w.bytes += int64(len(b))
	return len(b), nil
}
func (w *discardResponse) Flush() {}
