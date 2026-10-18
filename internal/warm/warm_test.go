package warm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rendercache/rendercache/internal/render"
	"github.com/rendercache/rendercache/internal/snapshot"
	"github.com/rendercache/rendercache/internal/target"
)

type discoverFunc func(context.Context) ([]target.Page, error)

func (f discoverFunc) Discover(ctx context.Context) ([]target.Page, error) { return f(ctx) }

type renderFunc func(context.Context, target.Page) (render.Result, error)

func (f renderFunc) Render(ctx context.Context, p target.Page) (render.Result, error) {
	return f(ctx, p)
}
func opts() Options {
	return Options{RefreshInterval: time.Hour, DiscoveryInterval: time.Hour, RetryBase: 30 * time.Millisecond, RetryMax: 120 * time.Millisecond, Workers: 2, QueueSize: 2, MaxPages: 32, Tick: 2 * time.Millisecond}
}
func page(path string) target.Page {
	p, _ := target.Map("https://public.example", "http://127.0.0.1:8081", path)
	return p
}
func result(p target.Page) render.Result {
	return render.Result{HTML: "<h1>" + p.URL + "</h1>", Status: 200, FinalURL: p.URL}
}
func wait(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
func TestWarmDedupPersistenceAndRestart(t *testing.T) {
	pages := []target.Page{page("/a"), page("/b"), page("/a")}
	source := discoverFunc(func(context.Context) ([]target.Page, error) { return pages, nil })
	store, _ := snapshot.Open(t.TempDir(), 1<<20)
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	}) // Windows cannot delete a folder still held open
	var mu sync.Mutex
	calls := map[string]int{}
	engine := renderFunc(func(ctx context.Context, p target.Page) (render.Result, error) {
		mu.Lock()
		calls[p.URL]++
		mu.Unlock()
		return result(p), nil
	})
	m, _ := New(opts(), source, engine, store)
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	wait(t, func() bool { s := m.Status(); return s.Cached == 2 && s.State == "ready" })
	m.Close()
	mu.Lock()
	for _, n := range calls {
		if n != 1 {
			t.Fatal("duplicate render", calls)
		}
	}
	mu.Unlock()
	for _, p := range pages {
		if _, err := store.Load(p); err != nil {
			t.Fatal(err)
		}
	}
	var repeat atomic.Int32
	second, _ := New(opts(), source, renderFunc(func(context.Context, target.Page) (render.Result, error) {
		repeat.Add(1)
		return render.Result{}, errors.New("should reuse recent disk snapshot")
	}), store)
	second.Start(context.Background())
	defer second.Close()
	wait(t, func() bool { return second.Status().State == "ready" })
	second.Close()
	if repeat.Load() != 0 {
		t.Fatal("restart re-rendered fresh snapshots")
	}
	if m.Start(context.Background()) == nil {
		t.Fatal("manager restarted after close")
	}
}
func TestBoundedQueueConcurrencyAndShutdown(t *testing.T) {
	pages := []target.Page{}
	for i := range 20 {
		pages = append(pages, page(fmt.Sprintf("/%02d", i)))
	}
	entered := make(chan struct{}, 20)
	var running atomic.Int32
	var peak atomic.Int32
	engine := renderFunc(func(ctx context.Context, p target.Page) (render.Result, error) {
		n := running.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		defer running.Add(-1)
		<-ctx.Done()
		return render.Result{}, ctx.Err()
	})
	store, _ := snapshot.Open(t.TempDir(), 1024)
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	}) // Windows cannot delete a folder still held open
	o := opts()
	o.QueueSize = 3
	m, _ := New(o, discoverFunc(func(context.Context) ([]target.Page, error) { return pages, nil }), engine, store)
	m.Start(context.Background())
	wait(t, func() bool { s := m.Status(); return s.Rendering == 2 && s.Queued == 3 })
	if peak.Load() != 2 || len(entered) != 2 {
		t.Fatal("worker limit violated")
	}
	started := time.Now()
	m.Close()
	if time.Since(started) > 5*time.Second || running.Load() != 0 || m.Status().State != "stopped" || m.Status().RenderFailures != 0 {
		t.Fatal("shutdown leaked or counted cancellation as failure", time.Since(started), running.Load(), m.Status().State, m.Status().RenderFailures)
	}
}
func TestRetryBackoffPreservesStaleAndRecovers(t *testing.T) {
	dir := t.TempDir()
	store, _ := snapshot.Open(dir, 1024)
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	}) // Windows cannot delete a folder still held open
	p := page("/a")
	saved, _ := store.Save(p, render.Result{HTML: "<h1>original</h1>", Status: 200})
	saved.CreatedAt = time.Now().Add(-time.Hour)
	b, _ := json.Marshal(saved)
	os.WriteFile(filepath.Join(dir, p.Key()+".json"), b, 0600)
	var mu sync.Mutex
	allowSuccess := make(chan struct{})
	attempts := []time.Time{}
	engine := renderFunc(func(ctx context.Context, _ target.Page) (render.Result, error) {
		mu.Lock()
		attempts = append(attempts, time.Now())
		n := len(attempts)
		mu.Unlock()
		if n <= 2 {
			return render.Result{}, errors.New("temporary renderer failure")
		}
		select {
		case <-allowSuccess:
		case <-ctx.Done():
			return render.Result{}, ctx.Err()
		}
		return result(p), nil
	})
	o := opts()
	o.RefreshInterval = time.Second
	m, _ := New(o, discoverFunc(func(context.Context) ([]target.Page, error) { return []target.Page{p}, nil }), engine, store)
	m.Start(context.Background())
	defer m.Close()
	wait(t, func() bool { return m.Status().RenderFailures >= 1 })
	s := m.Status()
	if s.Cached != 1 || s.Stale != 1 || s.Failed != 1 {
		t.Fatal(s)
	}
	old, err := store.Load(p)
	if err != nil || old.Result.HTML != "<h1>original</h1>" {
		t.Fatal("failed render destroyed stale snapshot", err)
	}
	close(allowSuccess)
	wait(t, func() bool { return m.Status().RenderSuccesses == 1 })
	m.Close()
	s = m.Status()
	if s.Failed != 0 || s.Fresh != 1 || s.RenderFailures != 2 {
		t.Fatal(s)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 3 || attempts[1].Sub(attempts[0]) < o.RetryBase || attempts[2].Sub(attempts[1]) < 2*o.RetryBase {
		t.Fatal("retry backoff not applied", attempts)
	}
}
func TestDiscoveryFailureKeepsPriorCoverageAndRecovers(t *testing.T) {
	var fail atomic.Bool
	p := page("/a")
	store, _ := snapshot.Open(t.TempDir(), 1024)
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	}) // Windows cannot delete a folder still held open
	source := discoverFunc(func(context.Context) ([]target.Page, error) {
		if fail.Load() {
			return nil, errors.New("sitemap unavailable")
		}
		return []target.Page{p}, nil
	})
	o := opts()
	o.DiscoveryInterval = 20 * time.Millisecond
	m, _ := New(o, source, renderFunc(func(context.Context, target.Page) (render.Result, error) { return result(p), nil }), store)
	m.Start(context.Background())
	defer m.Close()
	wait(t, func() bool { return m.Status().State == "ready" })
	fail.Store(true)
	wait(t, func() bool { return m.Status().DiscoveryError != "" })
	s := m.Status()
	if s.Discovered != 1 || s.Cached != 1 || s.State != "degraded" {
		t.Fatal(s)
	}
	fail.Store(false)
	wait(t, func() bool { return m.Status().DiscoveryError == "" })
	if m.Status().RenderSuccesses != 1 {
		t.Fatal("rediscovery re-rendered a fresh snapshot")
	}
}
func TestDiscoveryCancellationAndLimits(t *testing.T) {
	store, _ := snapshot.Open(t.TempDir(), 1024)
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	}) // Windows cannot delete a folder still held open
	source := discoverFunc(func(ctx context.Context) ([]target.Page, error) { <-ctx.Done(); return nil, ctx.Err() })
	m, _ := New(opts(), source, renderFunc(func(context.Context, target.Page) (render.Result, error) {
		t.Error("render called before discovery")
		return render.Result{}, nil
	}), store)
	m.Start(context.Background())
	wait(t, func() bool { return m.Status().Discovering })
	m.Close()
	if m.Status().Discovering {
		t.Fatal("discovery left running")
	}
	o := opts()
	o.MaxPages = 1
	over, _ := New(o, discoverFunc(func(context.Context) ([]target.Page, error) { return []target.Page{page("/a"), page("/b")}, nil }), renderFunc(func(context.Context, target.Page) (render.Result, error) { return render.Result{}, nil }), store)
	over.Start(context.Background())
	defer over.Close()
	wait(t, func() bool { return over.Status().DiscoveryError != "" })
	if over.Status().Discovered != 0 {
		t.Fatal("partial oversized discovery installed")
	}
}

func TestRefreshRunsOnSchedule(t *testing.T) {
	p := page("/scheduled")
	store, _ := snapshot.Open(t.TempDir(), 1024)
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	}) // Windows cannot delete a folder still held open
	var mu sync.Mutex
	calls := []time.Time{}
	engine := renderFunc(func(context.Context, target.Page) (render.Result, error) {
		mu.Lock()
		calls = append(calls, time.Now())
		mu.Unlock()
		return result(p), nil
	})
	o := opts()
	o.RefreshInterval = 40 * time.Millisecond
	m, _ := New(o, discoverFunc(func(context.Context) ([]target.Page, error) { return []target.Page{p}, nil }), engine, store)
	m.Start(context.Background())
	defer m.Close()
	wait(t, func() bool { return m.Status().RenderSuccesses >= 2 })
	m.Close()
	mu.Lock()
	defer mu.Unlock()
	// A slow runner can finish a further, correctly spaced refresh before Close
	// stops the manager, so check spacing rather than an exact count. An early or
	// duplicated refresh shows up as a gap shorter than the interval.
	if len(calls) < 2 {
		t.Fatal("refresh did not run", calls)
	}
	for i := 1; i < len(calls); i++ {
		if calls[i].Sub(calls[i-1]) < o.RefreshInterval {
			t.Fatal("refresh ran too early or duplicated", calls)
		}
	}
}

func TestQueueBackpressureDoesNotDropPages(t *testing.T) {
	pages := []target.Page{}
	for i := range 11 {
		pages = append(pages, page(fmt.Sprintf("/queued/%d", i)))
	}
	gate := make(chan struct{})
	var calls atomic.Int32
	engine := renderFunc(func(ctx context.Context, p target.Page) (render.Result, error) {
		select {
		case <-gate:
		case <-ctx.Done():
			return render.Result{}, ctx.Err()
		}
		calls.Add(1)
		return result(p), nil
	})
	store, _ := snapshot.Open(t.TempDir(), 1024)
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	}) // Windows cannot delete a folder still held open
	o := opts()
	o.Workers = 1
	o.QueueSize = 1
	m, _ := New(o, discoverFunc(func(context.Context) ([]target.Page, error) { return pages, nil }), engine, store)
	m.Start(context.Background())
	defer m.Close()
	wait(t, func() bool { s := m.Status(); return s.Rendering == 1 && s.Queued == 1 })
	close(gate)
	wait(t, func() bool { return m.Status().State == "ready" })
	m.Close()
	if m.Status().Cached != 11 || calls.Load() != 11 {
		t.Fatal("queue pressure dropped or duplicated pages", m.Status())
	}
}
func TestCorruptSnapshotIsRebuilt(t *testing.T) {
	dir := t.TempDir()
	store, _ := snapshot.Open(dir, 1024)
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	}) // Windows cannot delete a folder still held open
	p := page("/corrupt")
	os.WriteFile(filepath.Join(dir, p.Key()+".json"), []byte("invalid JSON"), 0600)
	m, _ := New(opts(), discoverFunc(func(context.Context) ([]target.Page, error) { return []target.Page{p}, nil }), renderFunc(func(context.Context, target.Page) (render.Result, error) { return result(p), nil }), store)
	m.Start(context.Background())
	defer m.Close()
	wait(t, func() bool { return m.Status().State == "ready" })
	m.Close()
	if _, err := store.Load(p); err != nil {
		t.Fatal("corrupt snapshot not repaired", err)
	}
}

func TestRequestAllowanceDedupAndRediscovery(t *testing.T) {
	store, _ := snapshot.Open(t.TempDir(), 1024)
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	}) // Windows cannot delete a folder still held open
	o := opts()
	o.MaxExtraPages = 1
	o.DiscoveryInterval = 10 * time.Millisecond
	gate := make(chan struct{})
	var calls atomic.Int32
	m, _ := New(o, discoverFunc(func(context.Context) ([]target.Page, error) { return nil, nil }), renderFunc(func(ctx context.Context, p target.Page) (render.Result, error) {
		calls.Add(1)
		select {
		case <-gate:
		case <-ctx.Done():
			return render.Result{}, ctx.Err()
		}
		return result(p), nil
	}), store)
	m.Start(context.Background())
	defer m.Close()
	for range 100 {
		if _, err := m.Request(page("/extra"), "cold"); err != nil {
			t.Fatal(err)
		}
	}
	wait(t, func() bool { return calls.Load() == 1 })
	if _, err := m.Request(page("/overflow"), "cold"); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	close(gate)
	wait(t, func() bool { return m.Status().RenderSuccesses == 1 })
	if m.Status().ExtraPages != 1 || m.Status().Discovered != 0 {
		t.Fatal(m.Status())
	}
	m.Close()
	if _, err := m.Request(page("/extra"), "purge"); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
}

func TestPurgeIntentSurvivesObservationUnderQueuePressure(t *testing.T) {
	store, _ := snapshot.Open(t.TempDir(), 1024)
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	}) // Windows cannot delete a folder still held open
	p := page("/target")
	record, _ := store.Save(p, result(p))
	gate := make(chan struct{})
	var targetRenders atomic.Int32
	o := opts()
	o.Workers = 1
	o.QueueSize = 1
	o.MaxExtraPages = 4
	engine := renderFunc(func(ctx context.Context, p target.Page) (render.Result, error) {
		if p.URL == page("/block").URL {
			select {
			case <-gate:
			case <-ctx.Done():
				return render.Result{}, ctx.Err()
			}
		}
		if p.URL == page("/target").URL {
			targetRenders.Add(1)
		}
		return result(p), nil
	})
	m, _ := New(o, discoverFunc(func(context.Context) ([]target.Page, error) { return nil, nil }), engine, store)
	m.Start(context.Background())
	defer m.Close()
	m.Request(page("/block"), "cold")
	wait(t, func() bool { return m.Status().Rendering == 1 })
	m.Request(page("/queued"), "cold")
	wait(t, func() bool { return m.Status().Queued == 1 })
	m.Request(p, "purge")
	m.Observe(p, record)
	close(gate)
	wait(t, func() bool { return targetRenders.Load() == 1 })
}

func TestPurgeRetiredInFlightPageGetsFollowup(t *testing.T) {
	store, _ := snapshot.Open(t.TempDir(), 1024)
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	}) // Windows cannot delete a folder still held open
	p := page("/retired")
	var listed atomic.Bool
	listed.Store(true)
	gate := make(chan struct{})
	var calls atomic.Int32
	o := opts()
	o.Workers = 1
	o.DiscoveryInterval = 10 * time.Millisecond
	o.MaxExtraPages = 1
	source := discoverFunc(func(context.Context) ([]target.Page, error) {
		if listed.Load() {
			return []target.Page{p}, nil
		}
		return nil, nil
	})
	engine := renderFunc(func(ctx context.Context, p target.Page) (render.Result, error) {
		n := calls.Add(1)
		if n == 1 {
			select {
			case <-gate:
			case <-ctx.Done():
				return render.Result{}, ctx.Err()
			}
		}
		return result(p), nil
	})
	m, _ := New(o, source, engine, store)
	m.Start(context.Background())
	defer m.Close()
	wait(t, func() bool { return calls.Load() == 1 })
	listed.Store(false)
	wait(t, func() bool { return m.Status().Discovered == 0 })
	if _, err := m.Request(p, "purge"); err != nil {
		t.Fatal(err)
	}
	close(gate)
	wait(t, func() bool { return calls.Load() == 2 })
	if m.Status().ExtraPages != 1 {
		t.Fatal("retired request not admitted")
	}
}

func TestRequestAllowanceEvictsIdlePages(t *testing.T) {
	store, _ := snapshot.Open(t.TempDir(), 1024)
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	}) // Windows cannot delete a folder still held open
	o := opts()
	o.MaxExtraPages = 2
	o.EvictAfter = time.Millisecond
	var calls atomic.Int32
	m, _ := New(o, discoverFunc(func(context.Context) ([]target.Page, error) { return nil, nil }), renderFunc(func(ctx context.Context, p target.Page) (render.Result, error) {
		calls.Add(1)
		return result(p), nil
	}), store)
	m.Start(context.Background())
	defer m.Close()
	for i, path := range []string{"/one", "/two"} {
		if _, err := m.Request(page(path), "cold"); err != nil {
			t.Fatal(err)
		}
		wait(t, func() bool { return m.Status().RenderSuccesses == uint64(i+1) })
	}
	m.Request(page("/one"), "stale") // /two is now the least recently requested
	wait(t, func() bool { return m.Status().RenderSuccesses == 3 })
	time.Sleep(5 * time.Millisecond) // both pages are now past EvictAfter
	if _, err := m.Request(page("/three"), "cold"); err != nil {
		t.Fatal("idle page not evicted:", err)
	}
	wait(t, func() bool { return m.Status().RenderSuccesses == 4 })
	s := m.Status()
	if s.ExtraPages != 2 || s.RequestEvictions != 1 || s.RequestRejections != 0 {
		t.Fatal(s)
	}
	for _, d := range s.ExtraDetails {
		if d.URL == page("/two").URL {
			t.Fatal("least recently requested page was kept")
		}
	}
	// The evicted page's snapshot is removed, so request-driven records on disk
	// stay within the allowance.
	wait(t, func() bool { _, err := store.Load(page("/two")); return err != nil })
	if _, err := store.Load(page("/one")); err != nil {
		t.Fatal("kept page lost its snapshot", err)
	}
}

// A page requested within EvictAfter keeps its slot, so a stream of new URLs
// cannot make Chrome render more than the allowance per EvictAfter.
func TestRecentRequestPagesAreNotEvicted(t *testing.T) {
	store, _ := snapshot.Open(t.TempDir(), 1024)
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
	}) // Windows cannot delete a folder still held open
	o := opts()
	o.MaxExtraPages = 1
	m, _ := New(o, discoverFunc(func(context.Context) ([]target.Page, error) { return nil, nil }), renderFunc(func(ctx context.Context, p target.Page) (render.Result, error) {
		return result(p), nil
	}), store)
	m.Start(context.Background())
	defer m.Close()
	m.Request(page("/one"), "cold")
	wait(t, func() bool { return m.Status().RenderSuccesses == 1 })
	if _, err := m.Request(page("/two"), "cold"); !errors.Is(err, ErrCapacity) {
		t.Fatal("recently requested page was evicted:", err)
	}
	if s := m.Status(); s.RequestEvictions != 0 || s.RequestRejections != 1 {
		t.Fatal(s)
	}
}
