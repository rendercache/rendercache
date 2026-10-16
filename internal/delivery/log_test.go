package delivery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRotationBoundedFieldsAndDrain(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, 4096, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 90; i++ {
		l.Record(Event{Time: time.Now(), Crawler: strings.Repeat("x", 2000), Path: "/page?token=secret", Cache: "HIT", Status: 200})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := l.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s := l.Status()
	if s.Accepted != 90 || s.Written != 90 || s.Failures != 0 || len(s.Recent) != 90 {
		t.Fatal(s)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Fatal(entries)
	}
	for _, entry := range entries {
		b, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil || len(b) > 4096 || strings.Contains(string(b), "secret") {
			t.Fatal(entry.Name(), len(b), err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var event Event
			if json.Unmarshal([]byte(line), &event) != nil || len(event.Crawler) > 128 || event.Path != "/page" {
				t.Fatal(line)
			}
		}
	}
	l.Record(Event{})
	if l.Status().Dropped != 1 {
		t.Fatal("closed logger did not count dropped record")
	}
}
func TestBackpressureAndCloseDeadline(t *testing.T) {
	// No consumer: deterministic stand-in for a stalled filesystem. Producers
	// remain bounded, and shutdown observes its context instead of waiting forever.
	l := &Log{queue: make(chan Event, 1), done: make(chan struct{}), state: Status{Enabled: true}}
	started := time.Now()
	for i := 0; i < 10000; i++ {
		l.Record(Event{})
	}
	if time.Since(started) > time.Second || l.Status().Dropped != 9999 {
		t.Fatal(l.Status())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if l.Close(ctx) != context.DeadlineExceeded {
		t.Fatal("close waited without deadline")
	}
}
func TestRotationFailureVisibleAndBounded(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "crawler.jsonl.1"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "crawler.jsonl.1", "block"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	l, err := Open(dir, 4096, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		l.Record(Event{Path: strings.Repeat("x", 512)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := l.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s := l.Status()
	if s.Failures == 0 || s.Error != "rotation failed" {
		t.Fatal(s)
	}
	info, _ := os.Stat(filepath.Join(dir, "crawler.jsonl"))
	if info.Size() > 4096 {
		t.Fatal(info.Size())
	}
}
