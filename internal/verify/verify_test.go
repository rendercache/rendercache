// SPDX-License-Identifier: Apache-2.0
package verify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rendercache/rendercache/internal/config"
)

type fakeDNS struct {
	ptr     map[string][]string
	forward map[string][]string
	err     error // returned by LookupAddr when set
	calls   atomic.Int32
	block   chan struct{}
}

func (f *fakeDNS) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	f.calls.Add(1)
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	if names, ok := f.ptr[addr]; ok {
		return names, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: addr, IsNotFound: true}
}

func (f *fakeDNS) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	var out []net.IPAddr
	for _, a := range f.forward[host] {
		out = append(out, net.IPAddr{IP: net.ParseIP(a)})
	}
	if len(out) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return out, nil
}

func TestParse(t *testing.T) {
	got, err := Parse([]byte(`{"creationTime":"x","prefixes":[{"ipv4Prefix":"192.0.2.0/24"},{"ipv6Prefix":"2001:db8::/48"},{"ipv4Prefix":"198.51.100.7"},{"ipv4Prefix":"203.0.113.9/24"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.0.2.0/24", "2001:db8::/48", "198.51.100.7/32", "203.0.113.0/24"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("prefix %d: got %s, want %s", i, got[i], want[i])
		}
	}
	for _, bad := range []string{``, `[]`, `{}`, `{"prefixes":[]}`, `{"prefixes":[{"ipv4Prefix":"not-an-ip"}]}`,
		`{"prefixes":[{"ipv4Prefix":"0.0.0.0/0"}]}`, `{"prefixes":[{"ipv6Prefix":"::/0"}]}`, `{"prefixes":[{"ipv4Prefix":"10.0.0.0/7"}]}`,
		`{"prefixes":[{"ipv6Prefix":"::ffff:192.0.2.0/120"}]}`, `{"prefixes":[{"other":"x"}]}`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func newVerifier(t *testing.T, o Options) *Verifier {
	t.Helper()
	v, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func load(v *Verifier, url string, prefixes ...string) {
	var ps []netip.Prefix
	for _, p := range prefixes {
		ps = append(ps, netip.MustParsePrefix(p))
	}
	v.lists[url].prefixes.Store(&ps)
}

func TestRanges(t *testing.T) {
	v := newVerifier(t, Options{Sources: []Source{{Crawler: "GPTBot", RangesURL: "https://ranges.test/gptbot.json"}}, Resolver: &fakeDNS{}})
	ctx := context.Background()
	if got := v.Check(ctx, "GPTBot", netip.MustParseAddr("203.0.113.9")); got != Unverified {
		t.Fatalf("no list loaded yet: got %s, want unverified", got)
	}
	load(v, "https://ranges.test/gptbot.json", "203.0.113.0/24", "2001:db8::/32")
	for addr, want := range map[string]Result{"203.0.113.9": Verified, "::ffff:203.0.113.9": Verified, "2001:db8::1": Verified, "198.51.100.7": Unverified} {
		if got := v.Check(ctx, "gptbot", netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: got %s, want %s", addr, got, want)
		}
	}
	if got := v.Check(ctx, "Slackbot", netip.MustParseAddr("203.0.113.9")); got != Unverifiable {
		t.Errorf("Slackbot: got %s", got)
	}
	if got := v.Check(ctx, "GPTBot", netip.Addr{}); got != Unverified {
		t.Errorf("no address: got %s", got)
	}
}

func TestReverseDNS(t *testing.T) {
	dns := &fakeDNS{
		ptr: map[string][]string{
			"66.249.66.1": {"crawl-66-249-66-1.googlebot.com."},
			"192.0.2.10":  {"crawl-1.evilgooglebot.com."},       // not under googlebot.com
			"192.0.2.11":  {"crawl-2.googlebot.com.evil.test."}, // suffix in the middle
			"192.0.2.12":  {"crawl-3.googlebot.com."},           // forward name points elsewhere
			"192.0.2.13":  {"googlebot.com."},                   // the bare domain is not a crawler host
		},
		forward: map[string][]string{
			"crawl-66-249-66-1.googlebot.com": {"66.249.66.1"},
			"crawl-1.evilgooglebot.com":       {"192.0.2.10"},
			"crawl-2.googlebot.com.evil.test": {"192.0.2.11"},
			"crawl-3.googlebot.com":           {"66.249.66.99"},
			"googlebot.com":                   {"192.0.2.13"},
		},
	}
	v := newVerifier(t, Options{Sources: []Source{{Crawler: "Googlebot", DNSSuffixes: []string{"googlebot.com", ".google.com."}}}, Resolver: dns})
	ctx := context.Background()
	for addr, want := range map[string]Result{"66.249.66.1": Verified, "192.0.2.10": Unverified, "192.0.2.11": Unverified, "192.0.2.12": Unverified, "192.0.2.13": Unverified, "192.0.2.99": Unverified} {
		if got := v.Check(ctx, "Googlebot", netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: got %s, want %s", addr, got, want)
		}
	}
	calls := dns.calls.Load()
	v.Check(ctx, "Googlebot", netip.MustParseAddr("66.249.66.1"))
	v.Check(ctx, "Googlebot", netip.MustParseAddr("192.0.2.10"))
	if dns.calls.Load() != calls {
		t.Error("answers were not cached")
	}
	v.now = func() time.Time { return time.Now().Add(7 * time.Hour) }
	v.Check(ctx, "Googlebot", netip.MustParseAddr("66.249.66.1"))
	if dns.calls.Load() == calls {
		t.Error("an expired answer was used")
	}
}

func TestDNSFailureNotCached(t *testing.T) {
	dns := &fakeDNS{err: &net.DNSError{Err: "server misbehaving", IsTemporary: true}}
	v := newVerifier(t, Options{Sources: []Source{{Crawler: "bingbot", DNSSuffixes: []string{"search.msn.com"}}}, Resolver: dns})
	addr := netip.MustParseAddr("157.55.39.1")
	if got := v.Check(context.Background(), "bingbot", addr); got != Unverified {
		t.Fatalf("got %s", got)
	}
	dns.err = nil
	dns.ptr = map[string][]string{"157.55.39.1": {"msnbot-157-55-39-1.search.msn.com."}}
	dns.forward = map[string][]string{"msnbot-157-55-39-1.search.msn.com": {"157.55.39.1"}}
	if got := v.Check(context.Background(), "bingbot", addr); got != Verified {
		t.Fatalf("a failed lookup was cached: got %s", got)
	}
}

func TestDNSBusyAndTimeout(t *testing.T) {
	dns := &fakeDNS{block: make(chan struct{})}
	v := newVerifier(t, Options{Sources: []Source{{Crawler: "YandexBot", DNSSuffixes: []string{"yandex.ru"}}}, Resolver: dns})
	var wg sync.WaitGroup
	for i := 0; i < dnsConcurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v.Check(context.Background(), "YandexBot", netip.AddrFrom4([4]byte{192, 0, 2, byte(i + 1)}))
		}(i)
	}
	for dns.calls.Load() < dnsConcurrency {
		time.Sleep(time.Millisecond)
	}
	started := time.Now()
	if got := v.Check(context.Background(), "YandexBot", netip.MustParseAddr("192.0.2.200")); got != Unverified {
		t.Errorf("busy: got %s", got)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Error("a check waited for a lookup slot")
	}
	wg.Wait() // the blocked lookups end at the DNS timeout
	if time.Since(started) > dnsTimeout+time.Second {
		t.Error("a lookup outlived the DNS timeout")
	}
}

func TestRefreshKeepsGoodList(t *testing.T) {
	body := `{"prefixes":[{"ipv4Prefix":"203.0.113.0/24"}]}`
	status := http.StatusOK
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	dir := t.TempDir()
	src := []Source{{Crawler: "PerplexityBot", RangesURL: srv.URL + "/perplexitybot.json"}}
	v := newVerifier(t, Options{Sources: src, Dir: dir, Client: srv.Client(), Resolver: &fakeDNS{}})
	if err := v.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	in := netip.MustParseAddr("203.0.113.5")
	if v.Check(context.Background(), "PerplexityBot", in) != Verified {
		t.Fatal("list not used")
	}
	for _, change := range []func(){
		func() { body = `{"prefixes":[]}` },
		func() { body = `<html>maintenance</html>` },
		func() {
			status, body = http.StatusServiceUnavailable, `{"prefixes":[{"ipv4Prefix":"198.51.100.0/24"}]}`
		},
		func() {
			status, body = http.StatusOK, `{"prefixes":[{"ipv4Prefix":"198.51.100.0/24"}]}`+strings.Repeat(" ", maxListBytes)
		},
	} {
		mu.Lock()
		change()
		mu.Unlock()
		if err := v.Refresh(context.Background()); err == nil {
			t.Error("a bad list was accepted")
		}
		if v.Check(context.Background(), "PerplexityBot", in) != Verified {
			t.Fatal("a failed refresh dropped the good list")
		}
	}
	st := v.Status()
	if len(st.Lists) != 1 || st.Lists[0].Prefixes != 1 || st.Lists[0].LastError == "" {
		t.Errorf("status %+v", st)
	}

	// A new process starts from the list kept on disk, before any fetch.
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(files) != 1 {
		t.Fatalf("kept files: %v", files)
	}
	offline := newVerifier(t, Options{Sources: src, Dir: dir, Client: &http.Client{Transport: failing{}}, Resolver: &fakeDNS{}, Refresh: time.Hour, Retry: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	offline.Start(ctx)
	if offline.Check(ctx, "PerplexityBot", in) != Verified {
		t.Error("kept list not loaded at start")
	}
	os.WriteFile(files[0], []byte("garbage"), 0o600)
	broken := newVerifier(t, Options{Sources: src, Dir: dir, Client: &http.Client{Transport: failing{}}, Resolver: &fakeDNS{}, Refresh: time.Hour, Retry: time.Hour})
	broken.Start(ctx)
	if broken.Check(ctx, "PerplexityBot", in) != Unverified {
		t.Error("a damaged kept list was trusted")
	}
}

type failing struct{}

func (failing) RoundTrip(*http.Request) (*http.Response, error) { return nil, errors.New("offline") }

func TestRedirectsStayOnHTTPS(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"prefixes":[{"ipv4Prefix":"198.51.100.0/24"}]}`))
	}))
	defer plain.Close()
	v := newVerifier(t, Options{Sources: []Source{{Crawler: "GPTBot", RangesURL: "https://ranges.test/gptbot.json"}}})
	req, _ := http.NewRequest("GET", plain.URL, nil)
	if err := v.client.CheckRedirect(req, []*http.Request{req}); err == nil {
		t.Error("a redirect to plain HTTP was followed")
	}
}

func TestNewRejects(t *testing.T) {
	for _, s := range [][]Source{
		{{Crawler: ""}},
		{{Crawler: "X"}},
		{{Crawler: "X", DNSSuffixes: []string{"com"}}},
		{{Crawler: "X", RangesURL: "ftp://x.test/a"}},
		{{Crawler: "X", DNSSuffixes: []string{"x.test"}}, {Crawler: "x", DNSSuffixes: []string{"x.test"}}},
	} {
		if _, err := New(Options{Sources: s}); err == nil {
			t.Errorf("accepted %+v", s)
		}
	}
}

// Every built-in source names a crawler of the default bots list, so turning
// verification on covers them without further configuration; shared lists
// are fetched once.
func TestBuiltin(t *testing.T) {
	v := newVerifier(t, Options{Sources: Builtin})
	bots := map[string]bool{}
	for _, b := range config.Default().Bots {
		bots[strings.ToLower(b)] = true
	}
	for _, s := range Builtin {
		if !bots[strings.ToLower(s.Crawler)] {
			t.Errorf("%s is not in the default bots list", s.Crawler)
		}
		if s.RangesURL != "" && !strings.HasPrefix(s.RangesURL, "https://") {
			t.Errorf("%s: list not fetched over HTTPS", s.Crawler)
		}
	}
	if len(v.order) >= len(Builtin) {
		t.Errorf("%d lists for %d sources: shared lists fetched twice", len(v.order), len(Builtin))
	}
	if Builtin[0].DNSSuffixes[0] != "googlebot.com" {
		t.Error("New changed the built-in table")
	}
}

// TestLiveRanges fetches every built-in list from its operator. It runs only
// when RENDERCACHE_TEST_LIVE_RANGES=1 (the ranges-live workflow), since tests
// otherwise never touch the network.
func TestLiveRanges(t *testing.T) {
	if os.Getenv("RENDERCACHE_TEST_LIVE_RANGES") != "1" {
		t.Skip("set RENDERCACHE_TEST_LIVE_RANGES=1 to fetch the operators' lists")
	}
	v := newVerifier(t, Options{Sources: Builtin})
	err := v.Refresh(context.Background())
	for _, l := range v.Status().Lists {
		t.Logf("%s: %d prefixes %s", l.URL, l.Prefixes, l.LastError)
		fmt.Printf("::notice title=ranges::%s %d prefixes %s\n", l.URL, l.Prefixes, l.LastError)
	}
	if err != nil {
		t.Error(err)
	}
}
