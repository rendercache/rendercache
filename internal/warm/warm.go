// SPDX-License-Identifier: Apache-2.0
// Package warm schedules sitemap pages independently of visitor requests.
package warm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rendercache/rendercache/internal/render"
	"github.com/rendercache/rendercache/internal/snapshot"
	"github.com/rendercache/rendercache/internal/target"
)

type Discoverer interface {
	Discover(context.Context) ([]target.Page, error)
}
type Store interface {
	Load(target.Page) (snapshot.Record, error)
	Save(target.Page, render.Result) (snapshot.Record, error)
	Delete(target.Page) error
}
type Options struct {
	RefreshInterval   time.Duration
	DiscoveryInterval time.Duration
	RetryBase         time.Duration
	RetryMax          time.Duration
	Workers           int
	QueueSize         int
	MaxPages          int
	Tick              time.Duration
	MaxExtraPages     int
	// EvictAfter is how long a request-driven page must go unrequested before a
	// new URL may take its slot. It bounds how many new pages crawler traffic can
	// render: at most MaxExtraPages per EvictAfter. Defaults to the refresh
	// interval, capped at one hour.
	EvictAfter time.Duration
	Site       string
	Origin     string
}
type entry struct {
	page        target.Page
	sitemap     bool
	withdrawn   bool
	rerun       bool
	force       bool
	cached      bool
	created     time.Time
	due         time.Time
	queued      bool
	active      bool
	failures    int
	lastError   string
	lastAttempt time.Time
	lastRequest time.Time
}
type PageStatus struct {
	URL         string    `json:"url"`
	Cached      bool      `json:"cached"`
	State       string    `json:"state"`
	SnapshotAt  time.Time `json:"snapshot_at,omitempty"`
	NextAttempt time.Time `json:"next_attempt"`
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	Failures    int       `json:"consecutive_failures"`
	LastError   string    `json:"last_error,omitempty"`
}
type Status struct {
	Enabled           bool         `json:"enabled"`
	State             string       `json:"state"`
	ExtraDetails      []PageStatus `json:"extra_page_details"`
	ExtraOmitted      int          `json:"extra_pages_omitted"`
	ExtraFailed       int          `json:"extra_failed_pages"`
	ExtraPages        int          `json:"extra_pages"`
	RequestRejections uint64       `json:"request_rejections"`
	RequestEvictions  uint64       `json:"request_evictions"`
	Discovered        int          `json:"discovered"`
	Cached            int          `json:"cached"`
	Missing           int          `json:"missing"`
	Fresh             int          `json:"fresh"`
	Stale             int          `json:"stale"`
	Queued            int          `json:"queued"`
	Rendering         int          `json:"rendering"`
	Failed            int          `json:"failed_pages"`
	RenderSuccesses   uint64       `json:"render_successes"`
	RenderFailures    uint64       `json:"render_failures"`
	Discovering       bool         `json:"discovering"`
	LastDiscovery     time.Time    `json:"last_discovery,omitempty"`
	NextDiscovery     time.Time    `json:"next_discovery,omitempty"`
	DiscoveryError    string       `json:"discovery_error,omitempty"`
	Pages             []PageStatus `json:"pages"`
	PagesOmitted      int          `json:"pages_omitted"`
}
type Manager struct {
	opts           Options
	source         Discoverer
	engine         render.Engine
	store          Store
	mu             sync.Mutex
	entries        map[string]*entry
	extras         map[string]*entry
	retired        map[string]*entry
	ctx            context.Context
	cancel         context.CancelFunc
	started        bool
	stopped        bool
	wg             sync.WaitGroup
	jobs           chan *entry
	wake           chan struct{}
	discovering    bool
	lastDiscovery  time.Time
	nextDiscovery  time.Time
	discoveryError string
	scanned        bool
	active         int
	successes      uint64
	failures       uint64
	rejected       uint64
	evicted        uint64
	dropped        []target.Page
}

func New(o Options, source Discoverer, engine render.Engine, store Store) (*Manager, error) {
	if o.MaxExtraPages == 0 {
		o.MaxExtraPages = 256
	}
	if o.Tick == 0 {
		o.Tick = time.Second
	}
	if o.EvictAfter == 0 {
		o.EvictAfter = min(o.RefreshInterval, time.Hour)
	}
	if source == nil || engine == nil || store == nil || o.RefreshInterval <= 0 || o.DiscoveryInterval <= 0 || o.RetryBase <= 0 || o.RetryMax < o.RetryBase || o.MaxExtraPages < 1 || o.MaxExtraPages > 10000 || o.Workers < 1 || o.Workers > 16 || o.QueueSize < 1 || o.MaxPages < 1 || o.Tick <= 0 || o.EvictAfter < 0 {
		return nil, errors.New("invalid warming options")
	}
	return &Manager{opts: o, source: source, engine: engine, store: store, entries: map[string]*entry{}, extras: map[string]*entry{}, retired: map[string]*entry{}, jobs: make(chan *entry, o.QueueSize), wake: make(chan struct{}, 1)}, nil
}
func (m *Manager) Start(parent context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started || m.stopped {
		return errors.New("warming manager already started")
	}
	m.started = true
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	m.ctx = ctx
	m.wg.Add(m.opts.Workers + 2)
	for range m.opts.Workers {
		go func() { defer m.wg.Done(); m.worker(ctx) }()
	}
	go func() { defer m.wg.Done(); m.schedule(ctx) }()
	go func() { defer m.wg.Done(); m.discover(ctx) }()
	return nil
}
func (m *Manager) Close() {
	m.mu.Lock()
	if !m.started {
		m.stopped = true
		m.mu.Unlock()
		return
	}
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
	m.mu.Lock()
	m.stopped = true
	for _, e := range m.entries {
		e.queued = false
		e.active = false
	}
	m.mu.Unlock()
}
func (m *Manager) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
func backoff(base, max time.Duration, failures int) time.Duration {
	d := base
	for i := 1; i < failures && d < max; i++ {
		if d > max/2 {
			return max
		}
		d *= 2
	}
	if d > max {
		return max
	}
	return d
}
func (m *Manager) discover(ctx context.Context) {
	failures := 0
	for {
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		m.discovering = true
		m.mu.Unlock()
		pages, err := m.source.Discover(ctx)
		if err == nil && len(pages) > m.opts.MaxPages {
			err = errors.New("discovered page limit exceeded")
		}
		// Validate all identities before replacing a successful prior discovery.
		if err == nil {
			for _, p := range pages {
				if e := p.Validate(); e != nil {
					err = e
					break
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			next := make(map[string]*entry, len(pages))
			for _, p := range pages {
				if ctx.Err() != nil {
					return
				}
				key := p.Key()
				if _, ok := next[key]; ok {
					continue
				}
				m.mu.Lock()
				existing := m.lookup(key)
				m.mu.Unlock()
				if existing != nil {
					next[key] = existing
					continue
				}
				e := &entry{page: p, sitemap: true, due: time.Now()}
				record, loadErr := m.store.Load(p)
				if loadErr == nil {
					e.cached = true
					e.created = record.CreatedAt
					e.due = record.CreatedAt.Add(m.opts.RefreshInterval)
				} else if !errors.Is(loadErr, snapshot.ErrNotFound) {
					e.lastError = shortError(fmt.Errorf("load snapshot: %w", loadErr))
				}
				next[key] = e
			}
			m.mu.Lock()

			for key, e := range next {
				if current := m.lookup(key); current != nil {
					e = current
					next[key] = current
				}
				e.sitemap = true
				delete(m.extras, key)
				delete(m.retired, key)
			}
			for key, e := range m.entries {
				if next[key] == nil && (e.active || e.queued) {
					e.sitemap = false
					m.retired[key] = e
				}
			}
			m.entries = next
			m.scanned = true
			m.mu.Unlock()
			failures = 0
		} else {
			failures++
		}
		delay := m.opts.DiscoveryInterval
		if err != nil {
			delay = backoff(m.opts.RetryBase, m.opts.RetryMax, failures)
			if delay > m.opts.DiscoveryInterval {
				delay = m.opts.DiscoveryInterval
			}
		}
		m.mu.Lock()
		m.discovering = false
		m.lastDiscovery = time.Now().UTC()
		m.nextDiscovery = time.Now().Add(delay).UTC()
		if err != nil {
			m.discoveryError = shortError(err)
		} else {
			m.discoveryError = ""
		}
		m.mu.Unlock()
		m.notify()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (m *Manager) schedule(ctx context.Context) {
	tick := time.NewTicker(m.opts.Tick)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		m.dropEvicted()
		m.dispatch(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-tick.C:
		}
	}
}
func (m *Manager) dispatch(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	due := []*entry{}
	for _, e := range m.scheduled() {
		if !e.queued && !e.active && !now.Before(e.due) {
			due = append(due, e)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].sitemap != due[j].sitemap {
			return due[i].sitemap
		}
		if due[i].cached != due[j].cached {
			return !due[i].cached
		}
		if due[i].due.Equal(due[j].due) {
			return due[i].page.URL < due[j].page.URL
		}
		return due[i].due.Before(due[j].due)
	})
	for _, e := range due {
		select {
		case m.jobs <- e:
			e.queued = true
		default:
			return
		}
	}
}
func (m *Manager) worker(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		var e *entry
		select {
		case <-ctx.Done():
			return
		case e = <-m.jobs:
		}
		m.mu.Lock()
		if m.lookup(e.page.Key()) != e {
			m.mu.Unlock()
			m.notify()
			continue
		}
		e.queued = false
		e.force = false
		e.active = true
		e.lastAttempt = time.Now().UTC()
		m.active++
		m.mu.Unlock()
		m.notify()
		result, err := m.engine.Render(ctx, e.page)
		var record snapshot.Record
		if err == nil && ctx.Err() == nil {
			record, err = m.store.Save(e.page, result)
		}
		private := errors.Is(err, render.ErrPrivate)
		if private && ctx.Err() == nil {
			if deleteErr := m.store.Delete(e.page); deleteErr != nil {
				err = fmt.Errorf("withdraw private snapshot: %w", deleteErr)
			}
		}
		m.mu.Lock()
		e.active = false
		m.active--
		if private {
			e.withdrawn = true
			e.cached = false
			e.created = time.Time{}
		}
		if ctx.Err() == nil {
			if err != nil {
				m.failures++
				e.failures++
				e.lastError = shortError(err)
				e.due = time.Now().Add(backoff(m.opts.RetryBase, m.opts.RetryMax, e.failures))
			} else {
				m.successes++
				e.withdrawn = false
				e.failures = 0
				e.lastError = ""
				e.cached = true
				e.created = record.CreatedAt
				e.due = record.CreatedAt.Add(m.opts.RefreshInterval)
			}
		}

		if e.rerun && ctx.Err() == nil {
			e.rerun = false
			e.due = time.Now()
		}
		if m.retired[e.page.Key()] == e {
			delete(m.retired, e.page.Key())
		}
		m.mu.Unlock()
		m.notify()
	}
}
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	s := Status{ExtraPages: len(m.extras), RequestRejections: m.rejected, RequestEvictions: m.evicted, Enabled: true, State: "warming", Discovered: len(m.entries), Queued: len(m.jobs), Rendering: m.active, RenderSuccesses: m.successes, RenderFailures: m.failures, Discovering: m.discovering, LastDiscovery: m.lastDiscovery, NextDiscovery: m.nextDiscovery, DiscoveryError: m.discoveryError, Pages: []PageStatus{}}
	keys := make([]string, 0, len(m.entries))
	for key := range m.entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return m.entries[keys[i]].page.URL < m.entries[keys[j]].page.URL })
	for i, key := range keys {
		e := m.entries[key]
		state := "waiting"
		if e.cached {
			s.Cached++
			if now.Before(e.created.Add(m.opts.RefreshInterval)) {
				s.Fresh++
				state = "fresh"
			} else {
				s.Stale++
				state = "stale"
			}
		} else {
			s.Missing++
		}
		if e.lastError != "" {
			s.Failed++
			state = "failed"
		}
		if e.queued {
			state = "queued"
		}
		if e.active {
			state = "rendering"
		}
		if i < 100 {
			s.Pages = append(s.Pages, PageStatus{URL: e.page.URL, Cached: e.cached, State: state, SnapshotAt: e.created, NextAttempt: e.due, LastAttempt: e.lastAttempt, Failures: e.failures, LastError: e.lastError})
		} else {
			s.PagesOmitted++
		}
	}
	extraKeys := make([]string, 0, len(m.extras))
	for key := range m.extras {
		extraKeys = append(extraKeys, key)
	}
	sort.Slice(extraKeys, func(i, j int) bool { return m.extras[extraKeys[i]].page.URL < m.extras[extraKeys[j]].page.URL })
	s.ExtraDetails = []PageStatus{}
	for i, key := range extraKeys {
		e := m.extras[key]
		state := "waiting"
		if e.cached {
			state = "fresh"
			if !now.Before(e.created.Add(m.opts.RefreshInterval)) {
				state = "stale"
			}
		}
		if e.lastError != "" {
			state = "failed"
			s.ExtraFailed++
		}
		if e.queued {
			state = "queued"
		}
		if e.active {
			state = "rendering"
		}
		if i < 100 {
			s.ExtraDetails = append(s.ExtraDetails, PageStatus{URL: e.page.URL, Cached: e.cached, State: state, SnapshotAt: e.created, NextAttempt: e.due, LastAttempt: e.lastAttempt, Failures: e.failures, LastError: e.lastError})
		} else {
			s.ExtraOmitted++
		}
	}

	if m.scanned && s.Missing == 0 && s.Stale == 0 && s.Failed == 0 {
		s.State = "ready"
	}
	if m.discoveryError != "" || s.Failed > 0 {
		s.State = "degraded"
	}
	if !m.scanned && m.discovering {
		s.State = "discovering"
	}
	if m.stopped {
		s.State = "stopped"
		s.Queued = 0
		s.Discovering = false
	}
	return s
}

func shortError(err error) string {
	s := strings.TrimSpace(err.Error())
	if len(s) > 1024 {
		s = s[:1024] + "…"
	}
	return s
}

var ErrCapacity = errors.New("request-page allowance is full")
var ErrStopped = errors.New("warming is not running")

func (m *Manager) lookup(key string) *entry {
	if e := m.entries[key]; e != nil {
		return e
	}
	if e := m.extras[key]; e != nil {
		return e
	}
	return m.retired[key]
}
func (m *Manager) scheduled() []*entry {
	out := make([]*entry, 0, len(m.entries)+len(m.extras))
	for _, e := range m.entries {
		out = append(out, e)
	}
	for _, e := range m.extras {
		out = append(out, e)
	}
	return out
}

// Request never performs disk/network I/O. Normal misses respect failure backoff;
// an explicit purge can force one extra pass if a render is already in flight.
func (m *Manager) Request(p target.Page, reason string) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	if len(p.URL) > 8192 {
		return "", errors.New("page URL exceeds 8192 bytes")
	}
	if m.opts.Site != "" {
		expected, err := target.Map(m.opts.Site, m.opts.Origin, p.URL)
		if err != nil || expected != p {
			return "", errors.New("page is outside configured site")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil || m.ctx.Err() != nil || m.stopped {
		return "", ErrStopped
	}
	e := m.lookup(p.Key())
	if e == nil {
		if !m.makeRoom() {
			m.rejected++
			return "", ErrCapacity
		}
		e = &entry{page: p, due: time.Now()}
		m.extras[p.Key()] = e
	}

	if m.retired[p.Key()] == e {
		if !m.makeRoom() {
			m.rejected++
			return "", ErrCapacity
		}
		delete(m.retired, p.Key())
		m.extras[p.Key()] = e
	}
	e.lastRequest = time.Now()
	if reason == "cold" {
		e.cached = false
		e.created = time.Time{}
	}
	if e.active {
		if reason == "purge" {
			e.rerun = true
		}
		return "coalesced", nil
	}
	if e.queued {
		return "coalesced", nil
	}
	if reason == "purge" || e.failures == 0 {
		e.due = time.Now()
		e.force = true
	}
	m.notify()
	return "queued", nil
}

// makeRoom frees one request-page slot when the allowance is full by dropping
// the least recently requested idle entry that has gone unrequested for at
// least EvictAfter. Its snapshot is deleted by the scheduler, so request-driven
// snapshots on disk stay within the allowance. Entries with queued, running,
// pending-purge or withdrawn state are kept. The caller holds m.mu.
func (m *Manager) makeRoom() bool {
	if len(m.extras) < m.opts.MaxExtraPages {
		return true
	}
	var oldestKey string
	var oldest *entry
	cutoff := time.Now().Add(-m.opts.EvictAfter)
	for key, e := range m.extras {
		if e.queued || e.active || e.rerun || e.force || e.withdrawn || e.lastRequest.After(cutoff) {
			continue
		}
		if oldest == nil || e.lastRequest.Before(oldest.lastRequest) {
			oldestKey, oldest = key, e
		}
	}
	if oldest == nil {
		return false
	}
	delete(m.extras, oldestKey)
	m.dropped = append(m.dropped, oldest.page)
	m.evicted++
	return true
}

// dropEvicted deletes the snapshots of evicted pages that nobody has asked for
// again. It runs in the scheduler goroutine before dispatch, so a page that is
// requested again after the check cannot be rendered before its old snapshot
// is gone.
func (m *Manager) dropEvicted() {
	m.mu.Lock()
	pages := []target.Page{}
	for _, p := range m.dropped {
		if m.lookup(p.Key()) == nil {
			pages = append(pages, p)
		}
	}
	m.dropped = nil
	m.mu.Unlock()
	for _, p := range pages {
		_ = m.store.Delete(p)
	}
}
func (m *Manager) Observe(p target.Page, record snapshot.Record) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.lookup(p.Key())
	if e != nil && (!e.cached || record.CreatedAt.After(e.created)) {
		e.cached = true
		e.created = record.CreatedAt
		if e.failures == 0 && !e.active && !e.queued && !e.force {
			e.due = record.CreatedAt.Add(m.opts.RefreshInterval)
		}
	}
}

func (m *Manager) Suppressed(p target.Page) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.lookup(p.Key())
	return e != nil && e.withdrawn
}
