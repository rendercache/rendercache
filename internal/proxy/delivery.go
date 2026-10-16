// SPDX-License-Identifier: Apache-2.0
package proxy

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/rendercache/rendercache/internal/delivery"
)

type simulatedKey struct{}
type receipt struct {
	http.ResponseWriter
	status   int
	bytes    int64
	failed   bool
	expected int64
	cached   bool
}

func (w *receipt) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Upgraded tunnel bytes bypass HTTP body accounting. Preserve the protocol and
// label those receipts rather than presenting tunnel traffic as HTML bytes.
func (w *receipt) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, b, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.status = 101
	}
	return c, b, err
}
func (w *receipt) WriteHeader(s int) {
	if s >= 100 && s < 200 && s != 101 {
		w.ResponseWriter.WriteHeader(s)
		return
	}
	if w.status == 0 {
		w.status = s
		w.ResponseWriter.WriteHeader(s)
	}
}
func (w *receipt) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	n, e := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	if e != nil || n != len(b) {
		w.failed = true
	}
	return n, e
}
func crawler(r *http.Request, bots []string) string { // Classify can bypass before identifying a crawler.
	check := r.Clone(r.Context())
	check.Method = "GET"
	check.Header = make(http.Header)
	check.Header.Set("User-Agent", r.UserAgent())
	check.URL.Path = "/"
	return Classify(check, bots).Crawler
}
func (g *Gateway) SetDeliveryLog(l *delivery.Log) { g.deliveries = l }
func (g *Gateway) observe(w http.ResponseWriter, r *http.Request, route Route) (http.ResponseWriter, func()) {
	claim := crawler(r, g.cfg.Bots)
	if claim == "" || g.deliveries == nil {
		return w, func() {}
	}
	started := time.Now()
	rw := &receipt{ResponseWriter: w}
	return rw, func() {
		interrupted := recover()
		if interrupted != nil {
			rw.failed = true
		}
		status := rw.status
		if status == 0 && interrupted == nil {
			status = 200
		}
		age := int64(0)
		if rw.cached {
			age, _ = strconv.ParseInt(rw.Header().Get("Age"), 10, 64)
			route.Reason = ""
		}
		simulated, _ := r.Context().Value(simulatedKey{}).(bool)
		outcome := rw.Header().Get("X-RenderCache")
		if outcome == "" {
			outcome = route.Outcome
		}
		g.deliveries.Record(delivery.Event{Time: started.UTC(), Crawler: claim, Path: r.URL.EscapedPath(), Method: r.Method, Status: status, Cache: outcome, Reason: route.Reason, Age: age, Bytes: rw.bytes, DurationMS: time.Since(started).Milliseconds(), Cached: rw.cached, FullHTML: rw.cached && status == 200 && r.Method == "GET" && !rw.failed && rw.expected > 0 && rw.bytes == rw.expected, Simulated: simulated, WriteFailed: rw.failed, Upgraded: rw.status == 101})
		if interrupted != nil {
			panic(interrupted)
		}
	}
}
func (g *Gateway) closeDeliveries() {
	if g.deliveries != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = g.deliveries.Close(ctx)
	}
}

func (g *Gateway) FlushDeliveryLog(ctx context.Context) error {
	if g.deliveries == nil {
		return nil
	}
	return g.deliveries.Close(ctx)
}
