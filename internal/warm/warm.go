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
}
type entry struct {
	page        target.Page
	cached      bool
	created     time.Time
	due         time.Time
	queued      bool
	active      bool
	failures    int
	lastError   string
	lastAttempt time.Time
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
	Enabled         bool         `json:"enabled"`
	State           string       `json:"state"`
	Discovered      int          `json:"discovered"`
	Cached          int          `json:"cached"`
	Missing         int          `json:"missing"`
	Fresh           int          `json:"fresh"`
	Stale           int          `json:"stale"`
	Queued          int          `json:"queued"`
	Rendering       int          `json:"rendering"`
	Failed          int          `json:"failed_pages"`
	RenderSuccesses uint64       `json:"render_successes"`
	RenderFailures  uint64       `json:"render_failures"`
	Discovering     bool         `json:"discovering"`
	LastDiscovery   time.Time    `json:"last_discovery,omitempty"`
	NextDiscovery   time.Time    `json:"next_discovery,omitempty"`
	DiscoveryError  string       `json:"discovery_error,omitempty"`
	Pages           []PageStatus `json:"pages"`
	PagesOmitted    int          `json:"pages_omitted"`
}
type Manager struct {
	opts           Options
	source         Discoverer
	engine         render.Engine
	store          Store
	mu             sync.Mutex
	entries        map[string]*entry
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
}

func New(o Options, source Discoverer, engine render.Engine, store Store) (*Manager, error) {
	if o.Tick == 0 {
		o.Tick = time.Second
	}
	if source == nil || engine == nil || store == nil || o.RefreshInterval <= 0 || o.DiscoveryInterval <= 0 || o.RetryBase <= 0 || o.RetryMax < o.RetryBase || o.Workers < 1 || o.Workers > 16 || o.QueueSize < 1 || o.MaxPages < 1 || o.Tick <= 0 {
		return nil, errors.New("invalid warming options")
	}
	return &Manager{opts: o, source: source, engine: engine, store: store, entries: map[string]*entry{}, jobs: make(chan *entry, o.QueueSize), wake: make(chan struct{}, 1)}, nil
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
				existing := m.entries[key]
				m.mu.Unlock()
				if existing != nil {
					next[key] = existing
					continue
				}
				e := &entry{page: p, due: time.Now()}
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
	for _, e := range m.entries {
		if !e.queued && !e.active && !now.Before(e.due) {
			due = append(due, e)
		}
	}
	sort.Slice(due, func(i, j int) bool {
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
		if m.entries[e.page.Key()] != e {
			m.mu.Unlock()
			m.notify()
			continue
		}
		e.queued = false
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
		m.mu.Lock()
		e.active = false
		m.active--
		if ctx.Err() == nil {
			if err != nil {
				m.failures++
				e.failures++
				e.lastError = shortError(err)
				e.due = time.Now().Add(backoff(m.opts.RetryBase, m.opts.RetryMax, e.failures))
			} else {
				m.successes++
				e.failures = 0
				e.lastError = ""
				e.cached = true
				e.created = record.CreatedAt
				e.due = record.CreatedAt.Add(m.opts.RefreshInterval)
			}
		}
		m.mu.Unlock()
		m.notify()
	}
}
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	s := Status{Enabled: true, State: "warming", Discovered: len(m.entries), Queued: len(m.jobs), Rendering: m.active, RenderSuccesses: m.successes, RenderFailures: m.failures, Discovering: m.discovering, LastDiscovery: m.lastDiscovery, NextDiscovery: m.nextDiscovery, DiscoveryError: m.discoveryError, Pages: []PageStatus{}}
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
