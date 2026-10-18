// SPDX-License-Identifier: Apache-2.0
package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rendercache/rendercache/internal/target"
	"github.com/rendercache/rendercache/internal/warm"
)

func (g *Gateway) request(p target.Page, reason string) (string, error) {
	if g.warming == nil {
		return "", warm.ErrStopped
	}
	return g.warming.Request(p, reason)
}
func (g *Gateway) snapshot(w http.ResponseWriter, r *http.Request, route Route) bool {
	// Request Host/forwarding claims never select the namespace or origin.
	p, err := target.Map(g.cfg.SiteURL(), g.cfg.Origin, g.snapshotURI(r.URL.RequestURI()))
	if err != nil {
		return false
	}
	if g.warming != nil && g.warming.Suppressed(p) {
		_, _ = g.request(p, "cold")
		return false
	}
	record, err := g.store.Load(p)
	if err != nil {
		_, _ = g.request(p, "cold")
		return false
	}
	if g.warming != nil {
		g.warming.Observe(p, record)
	}
	outcome := "HIT"
	if time.Since(record.CreatedAt) >= g.ttl || record.CreatedAt.After(time.Now().Add(time.Minute)) {
		outcome = "STALE"
		_, _ = g.request(p, "stale")
		g.stale.Add(1)
	} else {
		g.hits.Add(1)
	}
	h := w.Header()
	h.Set("X-RenderCache", outcome)
	h.Set("Cache-Control", "private, no-cache")
	h.Set("Vary", "User-Agent, Cookie, Authorization, Accept")
	h.Set("ETag", record.ETag())
	h.Set("Last-Modified", record.CreatedAt.UTC().Format(http.TimeFormat))
	age := int64(time.Since(record.CreatedAt).Seconds())
	if age < 0 {
		age = 0
	}
	h.Set("Age", strconv.FormatInt(age, 10))
	for name, value := range record.Result.Headers {
		if strings.EqualFold(name, "Content-Language") || strings.EqualFold(name, "X-Robots-Tag") {
			h.Set(name, value)
		}
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	status := record.Result.Status
	if status == http.StatusOK && notModified(r, record.ETag(), record.CreatedAt) {
		status = http.StatusNotModified
	}
	if record.Result.Location != "" {
		h.Set("Location", record.Result.Location)
	}
	if status != http.StatusNotModified {
		h.Set("Content-Length", strconv.Itoa(len(record.Result.HTML)))
	}
	if receipt, ok := w.(*receipt); ok {
		receipt.cached = true
		receipt.expected = int64(len(record.Result.HTML))
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead && status != http.StatusNotModified {
		_, _ = w.Write([]byte(record.Result.HTML))
	}
	return true
}

// snapshotURI drops configured tracking parameters (utm_source, gclid and similar)
// so query variants share one snapshot. Remaining parameters keep their
// original order and escaping. The origin still receives the full URL on a miss.
func (g *Gateway) snapshotURI(uri string) string {
	path, query, ok := strings.Cut(uri, "?")
	if !ok || len(g.ignored) == 0 {
		return uri
	}
	kept := make([]string, 0, 4)
	for _, part := range strings.Split(query, "&") {
		name, _, _ := strings.Cut(part, "=")
		if decoded, err := url.QueryUnescape(name); err == nil {
			name = decoded
		}
		if part != "" && g.ignored[strings.ToLower(name)] {
			continue
		}
		kept = append(kept, part)
	}
	if len(kept) == 0 {
		return path
	}
	return path + "?" + strings.Join(kept, "&")
}

// mapPage maps an admin-supplied URL (purge, compare) to the same page a
// crawler request for it would use, with ignored query parameters removed.
func (g *Gateway) mapPage(raw string) (target.Page, error) {
	p, err := target.Map(g.cfg.SiteURL(), g.cfg.Origin, raw)
	if err != nil {
		return p, err
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return p, err
	}
	return target.Map(g.cfg.SiteURL(), g.cfg.Origin, g.snapshotURI(u.RequestURI()))
}
func notModified(r *http.Request, etag string, created time.Time) bool {
	if value := r.Header.Get("If-None-Match"); value != "" {
		for _, candidate := range strings.Split(value, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
				return true
			}
		}
		return false
	}
	if value := r.Header.Get("If-Modified-Since"); value != "" {
		t, err := http.ParseTime(value)
		return err == nil && !created.Truncate(time.Second).After(t)
	}
	return false
}
func (g *Gateway) purge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	failure := func(code int, message string) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if g.cfg.AdminToken == "" {
		failure(http.StatusServiceUnavailable, "configure an admin token before using purge")
		return
	}
	if g.warming == nil {
		failure(http.StatusServiceUnavailable, "warming is disabled")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["url"]) != 1 || query.Get("url") == "" {
		failure(400, "exactly one url is required")
		return
	}
	p, err := g.mapPage(query.Get("url"))
	if err != nil {
		failure(400, "URL is outside the configured public site")
		return
	}
	check, _ := http.NewRequest(http.MethodGet, p.URL, nil)
	check.Header.Set("User-Agent", "RenderCache-Purge")
	if ClassifyPaths(check, []string{"RenderCache-Purge"}, g.cfg.BypassPaths).Outcome != "MISS" {
		failure(400, "URL is not an eligible HTML page")
		return
	}
	state, err := g.request(p, "purge")
	if err != nil {
		code := 503
		if errors.Is(err, warm.ErrCapacity) {
			code = 429
		}
		failure(code, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"url": p.URL, "state": state, "preserves_existing_snapshot": true})
}
