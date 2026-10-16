// SPDX-License-Identifier: Apache-2.0
// Package delivery records bounded, best-effort response receipts. Enqueue never
// waits for disk; counters expose dropped records and write/rotation failures.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/rendercache/rendercache/internal/localfs"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Event struct {
	Time        time.Time `json:"time"`
	Crawler     string    `json:"crawler_claim"`
	Path        string    `json:"path"`
	Method      string    `json:"method"`
	Status      int       `json:"status"`
	Cache       string    `json:"cache"`
	Reason      string    `json:"reason,omitempty"`
	Age         int64     `json:"snapshot_age_seconds"`
	Bytes       int64     `json:"delivered_bytes"`
	DurationMS  int64     `json:"duration_ms"`
	Cached      bool      `json:"cached_response"`
	FullHTML    bool      `json:"full_snapshot_html"`
	Simulated   bool      `json:"simulated"`
	Upgraded    bool      `json:"protocol_upgrade"`
	WriteFailed bool      `json:"write_failed"`
}
type Status struct {
	Enabled  bool    `json:"enabled"`
	Accepted uint64  `json:"accepted"`
	Written  uint64  `json:"written"`
	Dropped  uint64  `json:"dropped"`
	Failures uint64  `json:"failures"`
	Error    string  `json:"error,omitempty"`
	Recent   []Event `json:"recent"`
}
type Log struct {
	mu      sync.Mutex
	state   Status
	queue   chan Event
	done    chan struct{}
	closed  bool
	root    *os.Root
	dir     string
	limit   int64
	backups int
}

func Open(dir string, limit int64, backups int) (*Log, error) {
	if limit < 4096 || limit > 1<<30 || backups < 1 || backups > 10 {
		return nil, errors.New("invalid delivery log limits")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("delivery directory must not be a symlink")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	f, err := localfs.OpenRegular(root, "crawler.jsonl", os.O_CREATE|os.O_APPEND|os.O_WRONLY)
	if err != nil {
		root.Close()
		return nil, err
	}
	l := &Log{root: root, queue: make(chan Event, 256), done: make(chan struct{}), dir: dir, limit: limit, backups: backups, state: Status{Enabled: true}}
	go l.run(f)
	return l, nil
}
func bound(s string, n int) string {
	s = strings.ToValidUTF8(s, "?")
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "")
	}
	return s
}
func (l *Log) Record(e Event) {
	e.Crawler = bound(e.Crawler, 128)
	e.Path = bound(strings.SplitN(strings.SplitN(e.Path, "?", 2)[0], "#", 2)[0], 512)
	e.Method = bound(e.Method, 16)
	e.Cache = bound(e.Cache, 16)
	e.Reason = bound(e.Reason, 64)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		l.state.Dropped++
		return
	}
	select {
	case l.queue <- e:
		l.state.Accepted++
		if len(l.state.Recent) == 100 {
			copy(l.state.Recent, l.state.Recent[1:])
			l.state.Recent = l.state.Recent[:99]
		}
		l.state.Recent = append(l.state.Recent, e)
	default:
		l.state.Dropped++
	}
}
func (l *Log) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.state
	s.Recent = append([]Event{}, s.Recent...)
	return s
}
func (l *Log) Close(ctx context.Context) error {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.queue)
	}
	l.mu.Unlock()
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		l.failure("shutdown deadline exceeded")
		return ctx.Err()
	}
}
func (l *Log) failure(operation string) {
	l.mu.Lock()
	l.state.Failures++
	l.state.Error = operation
	l.mu.Unlock()
}
func (l *Log) run(f *os.File) {
	defer close(l.done)
	defer l.root.Close()
	defer func() {
		if f != nil {
			if err := f.Close(); err != nil {
				l.failure("close failed")
			}
		}
	}()
	for e := range l.queue {
		b, _ := json.Marshal(e)
		b = append(b, '\n')
		if f == nil {
			var err error
			f, err = localfs.OpenRegular(l.root, "crawler.jsonl", os.O_CREATE|os.O_APPEND|os.O_WRONLY)
			if err != nil {
				l.failure("open failed")
				continue
			}
		}
		info, err := f.Stat()
		if err != nil {
			l.failure("stat failed")
			continue
		}
		if info.Size()+int64(len(b)) > l.limit {
			if err := f.Close(); err != nil {
				l.failure("close before rotation failed")
			}
			f = nil
			// Never append past the limit after a failed rotation.
			if err := l.rotate(); err != nil {
				l.failure("rotation failed")
				continue
			}
			f, err = localfs.OpenRegular(l.root, "crawler.jsonl", os.O_CREATE|os.O_APPEND|os.O_WRONLY)
			if err != nil {
				l.failure("open after rotation failed")
				continue
			}
		}
		n, err := f.Write(b)
		if err != nil || n != len(b) {
			l.failure("write failed")
			continue
		}
		l.mu.Lock()
		l.state.Written++
		l.state.Error = ""
		l.mu.Unlock()
	}
}
func (l *Log) rotate() error {
	for i := 0; i <= l.backups; i++ {
		name := "crawler.jsonl"
		if i > 0 {
			name += "." + strconv.Itoa(i)
		}
		info, err := l.root.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("non-regular log file")
		}
	}
	base := "crawler.jsonl"
	if err := l.root.Remove(base + "." + strconv.Itoa(l.backups)); err != nil && !os.IsNotExist(err) {
		return err
	}
	for i := l.backups - 1; i >= 1; i-- {
		if err := l.root.Rename(base+"."+strconv.Itoa(i), base+"."+strconv.Itoa(i+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return l.root.Rename(base, base+".1")
}
