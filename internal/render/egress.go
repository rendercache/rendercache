// SPDX-License-Identifier: Apache-2.0
package render

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rendercache/rendercache/internal/target"
)

// egressGuard constrains ALL browser HTTP(S) traffic, including worker targets
// that are not covered by a main-tab CDP Fetch subscription. This is defense in
// depth, not an OS sandbox; deploy with an outbound firewall for untrusted apps.
type egressGuard struct {
	allowed   map[string]bool
	server    *http.Server
	transport *http.Transport
	address   string
	blocked   atomic.Uint64
	mu        sync.Mutex
	tunnels   map[net.Conn]bool
	closed    bool
}

func startGuard(ctx context.Context, origins []string) (*egressGuard, error) {
	g := &egressGuard{allowed: map[string]bool{}, tunnels: map[net.Conn]bool{}}
	for _, raw := range origins {
		u, err := target.Root(raw)
		if err != nil {
			return nil, err
		}
		g.allowed[u.Scheme+"://"+u.Host] = true
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	g.address = ln.Addr().String()
	g.transport = http.DefaultTransport.(*http.Transport).Clone()
	g.transport.Proxy = nil
	g.transport.ResponseHeaderTimeout = 15 * time.Second
	g.server = &http.Server{Handler: g, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	go g.server.Serve(ln)
	return g, nil
}
func (g *egressGuard) Close() {
	g.server.Close()
	g.transport.CloseIdleConnections()
	g.mu.Lock()
	g.closed = true
	for c := range g.tunnels {
		c.Close()
	}
	g.mu.Unlock()
}
func (g *egressGuard) allow(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return false
	}
	root := u.Scheme + "://" + u.Host
	canonical, err := target.Root(root)
	return err == nil && g.allowed[canonical.String()]
}
func (g *egressGuard) deny(w http.ResponseWriter) {
	g.blocked.Add(1)
	http.Error(w, "render destination blocked", 403)
}
func (g *egressGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		g.connect(w, r)
		return
	}
	if !g.allow(r.URL.String()) || r.URL.Scheme != "http" || (r.Method != "GET" && r.Method != "HEAD") {
		g.deny(w)
		return
	}
	req := r.Clone(r.Context())
	req.RequestURI = ""
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")
	req.Header.Del("Connection")
	req.Header.Del("Upgrade")
	response, err := g.transport.RoundTrip(req)
	if err != nil {
		http.Error(w, "render upstream unavailable", 502)
		return
	}
	defer response.Body.Close()
	for k, values := range response.Header {
		if strings.EqualFold(k, "Connection") || strings.EqualFold(k, "Transfer-Encoding") {
			continue
		}
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(response.StatusCode)
	io.Copy(w, response.Body)
}
func (g *egressGuard) connect(w http.ResponseWriter, r *http.Request) {
	// CONNECT includes the port even when the URL's default port is implicit.
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		g.deny(w)
		return
	}
	root := "https://" + net.JoinHostPort(host, port)
	u, err := target.Root(root)
	if err != nil || !g.allowed[u.String()] {
		g.deny(w)
		return
	}
	if r.Header.Get("Upgrade") != "" {
		g.deny(w)
		return
	}
	upstream, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, "render upstream unavailable", 502)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "tunnel unsupported", 500)
		return
	}
	client, rw, err := hijacker.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		client.Close()
		upstream.Close()
		return
	}
	g.tunnels[client] = true
	g.tunnels[upstream] = true
	g.mu.Unlock()
	defer func() {
		client.Close()
		upstream.Close()
		g.mu.Lock()
		delete(g.tunnels, client)
		delete(g.tunnels, upstream)
		g.mu.Unlock()
	}()
	if _, err = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err = rw.Flush(); err != nil {
		return
	}
	done := make(chan struct{})
	go func() { io.Copy(upstream, rw); upstream.Close(); close(done) }()
	io.Copy(client, upstream)
	client.Close()
	<-done
}
