// SPDX-License-Identifier: Apache-2.0
package snapshot

import (
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/rendercache/rendercache/internal/render"
	"github.com/rendercache/rendercache/internal/target"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestQuotaPreservesOldRecordAndReaccountsOnRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenWithLimits(dir, 8192, Limits{MaxBytes: 4096, MaxFiles: 2})
	t.Cleanup(func() {
		if s != nil {
			s.Close()
		}
	}) // Windows cannot delete a folder still held open
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := testPage(t)
	old, err := s.Save(p, render.Result{Status: 200, HTML: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(p, render.Result{Status: 200, HTML: strings.Repeat("x", 4096)}); !errors.Is(err, ErrQuota) {
		t.Fatal(err)
	}
	got, err := s.Load(p)
	if err != nil || got.ContentHash != old.ContentHash {
		t.Fatal("rejected refresh changed old record", err)
	}
	other, _ := target.Map(p.Site, "http://127.0.0.1:8081", "/second")
	if _, err = s.Save(other, render.Result{Status: 410}); err != nil {
		t.Fatal(err)
	}
	third, _ := target.Map(p.Site, "http://127.0.0.1:8081", "/third")
	if _, err = s.Save(third, render.Result{Status: 404}); !errors.Is(err, ErrQuota) {
		t.Fatal(err)
	}
	before := s.Status()
	if before.Files != 2 || before.Rejected != 2 || before.Bytes <= 0 {
		t.Fatal(before)
	}
	reopened, err := OpenWithLimits(dir, 8192, Limits{MaxBytes: 4096, MaxFiles: 1})
	t.Cleanup(func() {
		if reopened != nil {
			reopened.Close()
		}
	}) // Windows cannot delete a folder still held open
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if state := reopened.Status(); !state.OverLimit || state.Files != 2 || state.Bytes != before.Bytes {
		t.Fatal(state)
	}
	// A lower quota still permits reads and explicit cleanup.
	if _, err = reopened.Load(p); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-2 * time.Hour)
	if err = os.Chtimes(filepath.Join(dir, p.Key()+".json"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "unrelated.json"), []byte("keep"), 0600)
	removed, err := reopened.Prune(time.Now().Add(-time.Hour))
	if err != nil || removed != 1 {
		t.Fatal(removed, err)
	}
	if _, err = reopened.Load(p); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "unrelated.json")); err != nil {
		t.Fatal(err)
	}
	if state := reopened.Status(); state.Files != 1 || state.OverLimit {
		t.Fatal(state)
	}
}
func TestConcurrentQuotaAdmission(t *testing.T) {
	s, err := OpenWithLimits(t.TempDir(), 1024, Limits{MaxBytes: 4096, MaxFiles: 3})
	t.Cleanup(func() {
		if s != nil {
			s.Close()
		}
	}) // Windows cannot delete a folder still held open
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, _ := target.Map("https://public.example", "http://127.0.0.1:8081", fmt.Sprintf("/p%d", i))
			_, err := s.Save(p, render.Result{Status: 200, HTML: "a complete body"})
			if err != nil && !errors.Is(err, ErrQuota) {
				t.Error(err)
			}
			if err == nil {
				if _, err = s.Load(p); err != nil {
					t.Error(err)
				}
			}
		}(i)
	}
	wg.Wait()
	status := s.Status()
	if status.Files != 3 || status.Bytes > 4096 || status.Rejected != 17 {
		t.Fatal(status)
	}
}
func TestFileConfinementAndAbandonedTemps(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private")
	os.WriteFile(outside, []byte("private"), 0600)
	s, err := Open(dir, 1024)
	t.Cleanup(func() {
		if s != nil {
			s.Close()
		}
	}) // Windows cannot delete a folder still held open
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := testPage(t)
	if err = os.Symlink(outside, filepath.Join(dir, p.Key()+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(p); err == nil {
		t.Fatal("symlink load accepted")
	}
	if err = s.Delete(p); err == nil {
		t.Fatal("symlink deletion accepted")
	}
	if other, err := Open(dir, 1024); err == nil {
		other.Close()
		t.Fatal("startup accepted disguised snapshot")
	}
	os.Remove(filepath.Join(dir, p.Key()+".json"))
	name := ".snapshot-" + rand.Text() + ".tmp"
	os.WriteFile(filepath.Join(dir, name), []byte("abandoned"), 0600)
	unknown := ".snapshot-not-ours.tmp"
	os.WriteFile(filepath.Join(dir, unknown), []byte("keep"), 0600)
	n, err := s.CleanupTemps()
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err = os.Stat(filepath.Join(dir, unknown)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "private" {
		t.Fatal("outside file changed")
	}
}

func TestConcurrentSaveReadDeleteAndPruneAccounting(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenWithLimits(dir, 1024, Limits{MaxBytes: 4096, MaxFiles: 4})
	t.Cleanup(func() {
		if s != nil {
			s.Close()
		}
	}) // Windows cannot delete a folder still held open
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := testPage(t)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 10 {
				switch i % 4 {
				case 0:
					_, err := s.Save(p, render.Result{Status: 200, HTML: fmt.Sprintf("complete-%d-%d", i, j)})
					if err != nil {
						t.Error(err)
					}
				case 1:
					_, err := s.Load(p)
					if err != nil && !errors.Is(err, ErrNotFound) {
						t.Error(err)
					}
				case 2:
					if err := s.Delete(p); err != nil {
						t.Error(err)
					}
				case 3:
					if _, err := s.Prune(time.Now().Add(time.Hour)); err != nil {
						t.Error(err)
					}
				}
			}
		}(i)
	}
	wg.Wait()
	entries, _ := os.ReadDir(dir)
	var bytes int64
	files := 0
	for _, entry := range entries {
		if recordName(entry.Name()) {
			info, _ := entry.Info()
			bytes += info.Size()
			files++
		}
	}
	status := s.Status()
	if status.Bytes != bytes || status.Files != files {
		t.Fatal(status, bytes, files)
	}
}
