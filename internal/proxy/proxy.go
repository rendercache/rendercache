// SPDX-License-Identifier: Apache-2.0
package proxy

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rendercache/rendercache/internal/config"
	"github.com/rendercache/rendercache/internal/delivery"
	"github.com/rendercache/rendercache/internal/snapshot"
	"github.com/rendercache/rendercache/internal/warm"
)

type routeKey struct{}
type Route struct {
	Outcome string
	Crawler string
	Reason  string
}

var defaultBypassPaths = []string{"/api"}

// Classify applies the default bypass paths. See ClassifyPaths.
func Classify(r *http.Request, bots []string) Route {
	return ClassifyPaths(r, bots, defaultBypassPaths)
}

// ClassifyPaths only admits public document requests to the snapshot path.
// User agents are claims, not verified crawler identities. Requests under a
// bypass path prefix (for example /api or /graphql) always go to the origin.
func ClassifyPaths(r *http.Request, bots, bypassPaths []string) Route {
	bypass := func(reason string) Route { return Route{Outcome: "BYPASS", Reason: reason} }
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return bypass("method")
	}
	if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" {
		return bypass("private")
	}
	if r.Header.Get("Range") != "" || r.Header.Get("Upgrade") != "" {
		return bypass("protocol")
	}
	if r.Header.Get("If-Match") != "" || r.Header.Get("If-Unmodified-Since") != "" {
		return bypass("precondition")
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Cache-Control")), "no-cache") || strings.Contains(strings.ToLower(r.Header.Get("Cache-Control")), "max-age=0") || strings.EqualFold(r.Header.Get("Pragma"), "no-cache") {
		return bypass("revalidate")
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Cache-Control")), "no-store") {
		return bypass("no-store")
	}
	for _, prefix := range bypassPaths {
		if underPrefix(r.URL.Path, prefix) {
			return bypass("api")
		}
	}
	ext := strings.ToLower(path.Ext(r.URL.Path))
	if ext != "" && ext != ".html" && ext != ".htm" {
		return bypass("asset")
	}
	accept := r.Header.Get("Accept")
	if accept != "" && !acceptsHTML(accept) {
		return bypass("accept")
	}
	ua := strings.ToLower(r.UserAgent())
	for _, bot := range bots {
		if strings.Contains(ua, strings.ToLower(bot)) {
			return Route{Outcome: "MISS", Crawler: bot, Reason: "snapshot-unavailable"}
		}
	}
	return bypass("human")
}

func underPrefix(p, prefix string) bool {
	prefix = strings.TrimSuffix(prefix, "/")
	return prefix != "" && (p == prefix || strings.HasPrefix(p, prefix+"/"))
}

// variesByClient reports whether the same URL can produce different responses
// for crawlers and visitors. Assets and bypassed API paths never take the
// snapshot path, so they do not need Vary headers that fragment shared caches.
func variesByClient(route Route) bool {
	return route.Reason != "asset" && route.Reason != "api"
}

func acceptsHTML(value string) bool {
	for _, item := range strings.Split(value, ",") {
		media, params, err := mime.ParseMediaType(strings.TrimSpace(item))
		if err != nil {
			continue
		}
		if q, ok := params["q"]; ok {
			quality, err := strconv.ParseFloat(q, 64)
			if err != nil || quality <= 0 || quality > 1 {
				continue
			}
		}
		if media == "text/html" || media == "application/xhtml+xml" || media == "*/*" || media == "text/*" {
			return true
		}
	}
	return false
}

type Gateway struct {
	admission   chan struct{}
	storage     *snapshot.Store
	diagnostics chan struct{}
	deliveries  *delivery.Log
	cfg         config.Config
	warming     *warm.Manager
	store       *snapshot.Store
	hits        atomic.Uint64
	stale       atomic.Uint64
	proxy       *httputil.ReverseProxy
	logger      *slog.Logger
	started     time.Time
	requests    atomic.Uint64
	misses      atomic.Uint64
	transport   *http.Transport
	ttl         time.Duration
	timeout     time.Duration
	trusted     []netip.Prefix
	ignored     map[string]bool
}

func New(c config.Config, logger *slog.Logger) (*Gateway, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	origin, _ := c.OriginURL()
	if logger == nil {
		logger = slog.Default()
	}
	timeout, _ := time.ParseDuration(c.OriginTimeout)
	ttl, _ := time.ParseDuration(c.SnapshotTTL)
	trusted, _ := c.TrustedPrefixes()
	ignored := make(map[string]bool, len(c.IgnoreQueryParams))
	for _, name := range c.IgnoreQueryParams {
		ignored[strings.ToLower(name)] = true
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = timeout
	transport.MaxResponseHeaderBytes = 64 << 10
	transport.MaxConnsPerHost = 64
	transport.MaxIdleConnsPerHost = 8
	transport.IdleConnTimeout = 60 * time.Second
	g := &Gateway{admission: make(chan struct{}, c.HTTPConcurrency), diagnostics: make(chan struct{}, 2), cfg: c, logger: logger, started: time.Now(), transport: transport, ttl: ttl, timeout: timeout, trusted: trusted, ignored: ignored}
	g.proxy = &httputil.ReverseProxy{
		Transport: transport,
		ErrorLog:  log.New(io.Discard, "", 0),
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(origin)
			// Rewrite starts without any inbound X-Forwarded-* headers. Keep the
			// client chain and scheme only when the immediate peer is a trusted
			// edge; otherwise the origin sees this hop as plain HTTP from the edge
			// and HTTPS-enforcing apps redirect crawlers back to the same URL.
			trusted := g.trustedPeer(pr.In)
			if trusted {
				if chain := pr.In.Header.Values("X-Forwarded-For"); len(chain) > 0 {
					pr.Out.Header["X-Forwarded-For"] = append([]string(nil), chain...)
				}
			}
			pr.SetXForwarded()
			if trusted {
				if proto := forwardedProto(pr.In.Header.Get("X-Forwarded-Proto")); proto != "" {
					pr.Out.Header.Set("X-Forwarded-Proto", proto)
				}
				if host := pr.In.Header.Get("X-Forwarded-Host"); validForwardedHost(host) {
					pr.Out.Header.Set("X-Forwarded-Host", host)
				}
			}
			// Never allow an upstream or downstream cache to confuse bot and human responses.
			pr.Out.Header.Del("X-RenderCache")
			pr.Out.Header.Del("X-RenderCache-Edge-Key")
		},
		ModifyResponse: func(resp *http.Response) error {
			route, _ := resp.Request.Context().Value(routeKey{}).(Route)
			resp.Header.Set("X-RenderCache", route.Outcome)
			if variesByClient(route) {
				resp.Header.Add("Vary", "User-Agent, Cookie, Authorization, Accept")
			}

			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			route, _ := r.Context().Value(routeKey{}).(Route)
			w.Header().Set("X-RenderCache", route.Outcome)
			if variesByClient(route) {
				w.Header().Add("Vary", "User-Agent, Cookie, Authorization, Accept")
			}
			var limit *http.MaxBytesError
			if errors.As(err, &limit) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			} else {
				http.Error(w, "origin unavailable", http.StatusBadGateway)
			}
		},
	}
	return g, nil
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := g.classify(r)
	w, finish := g.observe(w, r, route)
	defer finish()
	g.requests.Add(1)
	if g.cfg.EdgeToken != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-RenderCache-Edge-Key")), []byte(g.cfg.EdgeToken)) != 1 {
		w.Header().Set("X-RenderCache", "BYPASS")
		http.Error(w, "forbidden", 403)
		return
	}
	select {
	case g.admission <- struct{}{}:
		defer func() { <-g.admission }()
	default:
		w.Header().Set("X-RenderCache", "BYPASS")
		http.Error(w, "server busy", 503)
		return
	}
	if r.ContentLength > g.cfg.MaxRequestBytes {
		w.Header().Set("X-RenderCache", "BYPASS")
		http.Error(w, "request body too large", 413)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, g.cfg.MaxRequestBytes)
	if route.Outcome == "MISS" {
		if g.store != nil && g.snapshot(w, r, route) {
			return
		}
		g.misses.Add(1)
	}
	// Ignore untrusted diagnostic headers; the decision comes only from classification.
	bounded, cancel := context.WithTimeout(r.Context(), g.timeout)
	defer cancel()
	ctx := context.WithValue(bounded, routeKey{}, route)
	g.proxy.ServeHTTP(w, r.WithContext(ctx))
}

func (g *Gateway) classify(r *http.Request) Route {
	return ClassifyPaths(r, g.cfg.Bots, g.cfg.BypassPaths)
}

// trustedPeer reports whether forwarding headers from this request's immediate
// peer may be passed on. A matching edge token proves the request came through
// the configured edge; otherwise the peer address must be in trusted_proxies.
func (g *Gateway) trustedPeer(r *http.Request) bool {
	if g.cfg.EdgeToken != "" {
		return true // ServeHTTP has already rejected requests without the token.
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range g.trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// forwardedProto accepts only the first value and only http or https.
func forwardedProto(value string) string {
	first, _, _ := strings.Cut(value, ",")
	switch proto := strings.ToLower(strings.TrimSpace(first)); proto {
	case "http", "https":
		return proto
	}
	return ""
}

func validForwardedHost(host string) bool {
	return host != "" && len(host) <= 255 && !strings.ContainsAny(host, " ,/\\@?#\t")
}

// SetWarmer must be called before starting the HTTP listeners.
func (g *Gateway) SetWarmer(manager *warm.Manager) { g.warming = manager }

func (g *Gateway) SetStorage(store *snapshot.Store)       { g.storage = store }
func (g *Gateway) SetSnapshotStore(store *snapshot.Store) { g.store = store; g.storage = store }
func (g *Gateway) storageStatus() any {
	if g.storage == nil {
		return nil
	}
	return g.storage.Status()
}

func (g *Gateway) Close() { g.transport.CloseIdleConnections(); g.closeDeliveries() }

func (g *Gateway) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		warming := warm.Status{Enabled: false, State: "disabled"}
		if g.warming != nil {
			warming = g.warming.Status()
		}
		json.NewEncoder(w).Encode(map[string]any{"warming": warming, "bot_cache_hits": g.hits.Load(), "bot_cache_stale": g.stale.Load(), "delivery_log": g.deliveryStatus(), "storage": g.storageStatus(), "stage": "prototype-alpha", "manual_render_available": true, "origin": g.cfg.Origin, "uptime_seconds": int(time.Since(g.started).Seconds()), "requests": g.requests.Load(), "bot_cache_misses": g.misses.Load(), "rendering_enabled": g.warming != nil, "snapshot_serving_enabled": g.store != nil})
	})
	mux.HandleFunc("POST /purge", g.purge)
	mux.HandleFunc("GET /{$}", g.demo)
	mux.HandleFunc("GET /ui.js", g.demo)
	mux.HandleFunc("GET /ui.css", g.demo)
	mux.HandleFunc("POST /diagnose", g.diagnose)
	mux.HandleFunc("POST /simulate", g.simulate)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.cfg.AdminToken != "" && !g.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Basic realm="RenderCache", charset="UTF-8"`)
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
