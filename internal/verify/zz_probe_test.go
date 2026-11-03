package verify

import (
	"context"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A kept list whose file time is in the future never expires.
func TestProbeFutureMtime(t *testing.T) {
	dir := t.TempDir()
	src := []Source{{Crawler: "GPTBot", RangesURL: "https://ranges.test/g.json"}}
	v := newVerifier(t, Options{Sources: src, Dir: dir})
	os.MkdirAll(dir, 0o700)
	name := v.file("https://ranges.test/g.json")
	os.WriteFile(name, []byte(`{"prefixes":[{"ipv4Prefix":"203.0.113.0/24"}]}`), 0o600)
	future := time.Now().Add(365 * 24 * time.Hour)
	os.Chtimes(name, future, future)
	w := newVerifier(t, Options{Sources: src, Dir: dir, Client: &http.Client{Transport: failing{}}, Refresh: time.Hour, Retry: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	w.now = func() time.Time { return time.Now().Add(200 * 24 * time.Hour) }
	got := w.Check(ctx, "GPTBot", netip.MustParseAddr("203.0.113.9"))
	t.Logf("200 days later, list never refreshed, file mtime +1y: %s", got)
	st := w.Status()
	t.Logf("status: %+v", st.Lists[0])
	_ = filepath.Join
}

// An expired list: what does Status say, and what does Check answer?
func TestProbeExpiredStatus(t *testing.T) {
	v := newVerifier(t, Options{Sources: []Source{{Crawler: "GPTBot", RangesURL: "https://ranges.test/g.json"}}})
	load(v, "https://ranges.test/g.json", "203.0.113.0/24")
	l := v.lists["https://ranges.test/g.json"]
	l.mu.Lock()
	l.updated = time.Now().Add(-maxListAge - time.Hour)
	l.mu.Unlock()
	t.Logf("check: %s; status: %+v", v.Check(context.Background(), "GPTBot", netip.MustParseAddr("203.0.113.9")), v.Status().Lists[0])
}
