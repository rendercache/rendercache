// SPDX-License-Identifier: Apache-2.0

// Package verify checks that a request claiming to come from a crawler comes
// from that crawler's network: an address in the ranges its operator
// publishes, or a reverse DNS name in the operator's domain that resolves back
// to the same address. A user agent is only a claim.
package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Result is what a check found.
type Result string

const (
	Verified     Result = "verified"     // the address belongs to the crawler named
	Unverified   Result = "unverified"   // the crawler can be checked, and this address failed
	Unverifiable Result = "unverifiable" // the operator publishes no way to check it
)

// Resolver is the part of net.Resolver a check uses.
type Resolver interface {
	LookupAddr(ctx context.Context, addr string) ([]string, error)
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

type Options struct {
	Sources  []Source
	Dir      string       // where fetched lists are kept between runs; "" keeps none
	Client   *http.Client // nil: a client with a 30s timeout that follows https redirects only
	Resolver Resolver     // nil: net.DefaultResolver
	Refresh  time.Duration
	Retry    time.Duration
	Logger   *slog.Logger
}

const (
	maxListBytes   = 4 << 20
	maxPrefixes    = 100000
	maxCache       = 10000
	dnsTimeout     = 2 * time.Second
	dnsConcurrency = 16
	maxNames       = 5
	positiveTTL    = 6 * time.Hour
	negativeTTL    = 30 * time.Minute
)

type list struct {
	url      string
	crawlers []string
	prefixes atomic.Pointer[[]netip.Prefix]

	mu          sync.Mutex
	updated     time.Time
	lastAttempt time.Time
	lastError   string
}

type cached struct {
	ok      bool
	expires time.Time
}

type Verifier struct {
	sources  map[string]Source // by lower-case crawler name
	lists    map[string]*list  // by URL
	order    []string
	dir      string
	client   *http.Client
	resolver Resolver
	refresh  time.Duration
	retry    time.Duration
	logger   *slog.Logger
	dns      chan struct{}

	mu    sync.Mutex
	cache map[string]cached
	now   func() time.Time

	refreshing sync.Mutex
}

func New(o Options) (*Verifier, error) {
	v := &Verifier{sources: map[string]Source{}, lists: map[string]*list{}, dir: o.Dir, client: o.Client, resolver: o.Resolver,
		refresh: o.Refresh, retry: o.Retry, logger: o.Logger, dns: make(chan struct{}, dnsConcurrency), cache: map[string]cached{}, now: time.Now}
	if v.client == nil {
		v.client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || req.URL.Scheme != "https" {
				return errors.New("redirect refused")
			}
			return nil
		}}
	}
	if v.resolver == nil {
		v.resolver = net.DefaultResolver
	}
	if v.refresh <= 0 {
		v.refresh = 24 * time.Hour
	}
	if v.retry <= 0 {
		v.retry = 15 * time.Minute
	}
	if v.logger == nil {
		v.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	for _, s := range o.Sources {
		key := strings.ToLower(s.Crawler)
		if key == "" {
			return nil, errors.New("verify: source without a crawler name")
		}
		if _, dup := v.sources[key]; dup {
			return nil, fmt.Errorf("verify: %s listed twice", s.Crawler)
		}
		if s.RangesURL == "" && len(s.DNSSuffixes) == 0 {
			return nil, fmt.Errorf("verify: %s has no ranges and no DNS suffixes", s.Crawler)
		}
		s.DNSSuffixes = append([]string(nil), s.DNSSuffixes...)
		for i, suffix := range s.DNSSuffixes {
			s.DNSSuffixes[i] = strings.ToLower(strings.Trim(suffix, "."))
			if !strings.Contains(s.DNSSuffixes[i], ".") {
				return nil, fmt.Errorf("verify: %s: DNS suffix %q is not a domain", s.Crawler, suffix)
			}
		}
		v.sources[key] = s
		if s.RangesURL == "" {
			continue
		}
		u, err := url.Parse(s.RangesURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("verify: %s: ranges URL %q", s.Crawler, s.RangesURL)
		}
		l := v.lists[s.RangesURL]
		if l == nil {
			l = &list{url: s.RangesURL}
			v.lists[s.RangesURL] = l
			v.order = append(v.order, s.RangesURL)
		}
		l.crawlers = append(l.crawlers, s.Crawler)
	}
	return v, nil
}

// Verifiable reports whether the operator of crawler publishes a way to check it.
func (v *Verifier) Verifiable(crawler string) bool {
	_, ok := v.sources[strings.ToLower(crawler)]
	return ok
}

// Check says whether addr belongs to crawler. It never waits longer than the
// DNS timeout, and answers Unverified when it cannot tell: a crawler that
// fails a check gets the live site, which is always correct, only slower.
func (v *Verifier) Check(ctx context.Context, crawler string, addr netip.Addr) Result {
	s, ok := v.sources[strings.ToLower(crawler)]
	if !ok {
		return Unverifiable
	}
	addr = addr.Unmap()
	if !addr.IsValid() {
		return Unverified
	}
	if s.RangesURL != "" {
		if p := v.lists[s.RangesURL].prefixes.Load(); p != nil {
			for _, prefix := range *p {
				if prefix.Contains(addr) {
					return Verified
				}
			}
		}
	}
	if len(s.DNSSuffixes) == 0 {
		return Unverified
	}
	key := strings.ToLower(s.Crawler) + "|" + addr.String()
	v.mu.Lock()
	c, hit := v.cache[key]
	v.mu.Unlock()
	if hit && v.now().Before(c.expires) {
		if c.ok {
			return Verified
		}
		return Unverified
	}
	select {
	case v.dns <- struct{}{}:
		defer func() { <-v.dns }()
	default:
		return Unverified // too many lookups at once; this request gets the live site
	}
	lookup, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()
	ok, final := v.reverse(lookup, s, addr)
	if final {
		ttl := negativeTTL
		if ok {
			ttl = positiveTTL
		}
		v.remember(key, cached{ok: ok, expires: v.now().Add(ttl)})
	}
	if ok {
		return Verified
	}
	return Unverified
}

// reverse runs forward-confirmed reverse DNS. final is false when a lookup
// failed for a reason other than "no such name" (a timeout, a server error),
// so the answer is not kept.
func (v *Verifier) reverse(ctx context.Context, s Source, addr netip.Addr) (ok, final bool) {
	names, err := v.resolver.LookupAddr(ctx, addr.String())
	if err != nil {
		return false, notFound(err)
	}
	final = true
	for i, name := range names {
		if i == maxNames {
			break
		}
		name = strings.ToLower(strings.TrimSuffix(name, "."))
		if !inDomain(name, s.DNSSuffixes) {
			continue
		}
		ips, err := v.resolver.LookupIPAddr(ctx, name)
		if err != nil {
			if !notFound(err) {
				final = false
			}
			continue
		}
		for _, ip := range ips {
			if back, ok := netip.AddrFromSlice(ip.IP); ok && back.Unmap() == addr {
				return true, true
			}
		}
	}
	return false, final
}

func notFound(err error) bool {
	var d *net.DNSError
	return errors.As(err, &d) && d.IsNotFound
}

func inDomain(name string, suffixes []string) bool {
	for _, s := range suffixes {
		if strings.HasSuffix(name, "."+s) {
			return true
		}
	}
	return false
}

func (v *Verifier) remember(key string, c cached) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.cache) >= maxCache {
		now := v.now()
		for k, old := range v.cache {
			if now.After(old.expires) {
				delete(v.cache, k)
			}
		}
		for k := range v.cache {
			if len(v.cache) < maxCache*9/10 {
				break
			}
			delete(v.cache, k)
		}
	}
	v.cache[key] = c
}

// Start loads the lists kept from earlier runs, then fetches every list now
// and again each refresh interval (sooner after a failure) until ctx ends.
func (v *Verifier) Start(ctx context.Context) {
	for _, u := range v.order {
		v.loadKept(v.lists[u])
	}
	go func() {
		for {
			wait := v.refresh
			if v.Refresh(ctx) != nil {
				wait = v.retry
			}
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
	}()
}

// Refresh fetches every list now. A list that fails keeps its last good copy.
func (v *Verifier) Refresh(ctx context.Context) error {
	v.refreshing.Lock()
	defer v.refreshing.Unlock()
	var errs []error
	for _, u := range v.order {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := v.fetch(ctx, v.lists[u]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (v *Verifier) fetch(ctx context.Context, l *list) error {
	l.mu.Lock()
	l.lastAttempt = v.now()
	l.mu.Unlock()
	raw, prefixes, err := v.download(ctx, l.url)
	if err == nil {
		l.prefixes.Store(&prefixes)
		v.keep(l.url, raw)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		l.lastError = err.Error()
		v.logger.Warn("crawler ranges not updated", "url", l.url, "error", err)
		return err
	}
	l.lastError = ""
	l.updated = v.now()
	return nil
}

func (v *Verifier) download(ctx context.Context, address string) ([]byte, []netip.Prefix, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "RenderCache crawler-range fetch")
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", address, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("%s: HTTP %d", address, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", address, err)
	}
	if len(raw) > maxListBytes {
		return nil, nil, fmt.Errorf("%s: list larger than %d bytes", address, maxListBytes)
	}
	prefixes, err := Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", address, err)
	}
	return raw, prefixes, nil
}

// Parse reads the list format the operators share:
// {"prefixes":[{"ipv4Prefix":"192.0.2.0/24"},{"ipv6Prefix":"2001:db8::/32"}]}.
// A bare address counts as a single-address prefix.
func Parse(raw []byte) ([]netip.Prefix, error) {
	var doc struct {
		Prefixes []struct {
			IPv4 string `json:"ipv4Prefix"`
			IPv6 string `json:"ipv6Prefix"`
		} `json:"prefixes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("not a prefix list: %w", err)
	}
	if len(doc.Prefixes) == 0 {
		return nil, errors.New("empty prefix list")
	}
	if len(doc.Prefixes) > maxPrefixes {
		return nil, fmt.Errorf("more than %d prefixes", maxPrefixes)
	}
	out := make([]netip.Prefix, 0, len(doc.Prefixes))
	for _, entry := range doc.Prefixes {
		for _, text := range []string{entry.IPv4, entry.IPv6} {
			if text == "" {
				continue
			}
			p, err := parsePrefix(strings.TrimSpace(text))
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("empty prefix list")
	}
	return out, nil
}

func parsePrefix(text string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(text); err == nil {
		if p.Addr().Is4In6() {
			return netip.Prefix{}, fmt.Errorf("prefix %s: write IPv4 ranges as IPv4", text)
		}
		// A prefix wider than /8 (IPv4) or /16 (IPv6) is not a crawler's network.
		if (p.Addr().Is4() && p.Bits() < 8) || (p.Addr().Is6() && p.Bits() < 16) {
			return netip.Prefix{}, fmt.Errorf("prefix %s is too wide", text)
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(text)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("bad prefix %q", text)
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func (v *Verifier) file(address string) string {
	var b strings.Builder
	for _, r := range strings.TrimPrefix(strings.TrimPrefix(address, "https://"), "http://") {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return filepath.Join(v.dir, b.String())
}

func (v *Verifier) keep(address string, raw []byte) {
	if v.dir == "" {
		return
	}
	if err := os.MkdirAll(v.dir, 0o700); err != nil {
		v.logger.Warn("crawler ranges not kept", "error", err)
		return
	}
	name := v.file(address)
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		v.logger.Warn("crawler ranges not kept", "error", err)
		return
	}
	if err := os.Rename(tmp, name); err != nil {
		os.Remove(tmp)
		v.logger.Warn("crawler ranges not kept", "error", err)
	}
}

func (v *Verifier) loadKept(l *list) {
	if v.dir == "" {
		return
	}
	name := v.file(l.url)
	info, err := os.Stat(name)
	if err != nil || info.Size() > maxListBytes {
		return
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		return
	}
	prefixes, err := Parse(raw)
	if err != nil {
		v.logger.Warn("kept crawler ranges unreadable", "file", name, "error", err)
		return
	}
	l.prefixes.Store(&prefixes)
	l.mu.Lock()
	l.updated = info.ModTime()
	l.mu.Unlock()
}

type ListStatus struct {
	URL         string    `json:"url"`
	Crawlers    []string  `json:"crawlers"`
	Prefixes    int       `json:"prefixes"`
	Updated     time.Time `json:"updated,omitzero"`
	LastAttempt time.Time `json:"last_attempt,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
}

type Status struct {
	Lists       []ListStatus `json:"lists"`
	DNSOnly     []string     `json:"dns_only"`
	CachedNames int          `json:"cached_dns_results"`
}

func (v *Verifier) Status() Status {
	s := Status{Lists: []ListStatus{}, DNSOnly: []string{}}
	for _, u := range v.order {
		l := v.lists[u]
		ls := ListStatus{URL: u, Crawlers: append([]string(nil), l.crawlers...)}
		if p := l.prefixes.Load(); p != nil {
			ls.Prefixes = len(*p)
		}
		l.mu.Lock()
		ls.Updated, ls.LastAttempt, ls.LastError = l.updated, l.lastAttempt, l.lastError
		l.mu.Unlock()
		s.Lists = append(s.Lists, ls)
	}
	for _, src := range v.sources {
		if src.RangesURL == "" {
			s.DNSOnly = append(s.DNSOnly, src.Crawler)
		}
	}
	sort.Strings(s.DNSOnly)
	v.mu.Lock()
	s.CachedNames = len(v.cache)
	v.mu.Unlock()
	return s
}
