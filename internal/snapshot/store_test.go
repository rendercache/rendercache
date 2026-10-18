package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/rendercache/rendercache/internal/render"
	"github.com/rendercache/rendercache/internal/target"
)

func testPage(t *testing.T) target.Page {
	t.Helper()
	p, err := target.Map("https://public.example", "http://127.0.0.1:8081", "/page?q=1")
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestPersistenceAndCorruption(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 1024)
	t.Cleanup(func() {
		if s != nil {
			s.Close()
		}
	}) // Windows cannot delete a folder still held open
	if err != nil {
		t.Fatal(err)
	}
	p := testPage(t)
	if _, err = s.Load(p); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	saved, err := s.Save(p, render.Result{HTML: "<!doctype html><h1>Saved</h1>", Status: 200, FinalURL: p.URL})
	if err != nil {
		t.Fatal(err)
	}
	reopened, _ := Open(dir, 1024)
	t.Cleanup(func() {
		if reopened != nil {
			reopened.Close()
		}
	}) // Windows cannot delete a folder still held open
	loaded, err := reopened.Load(p)
	if err != nil || saved.ContentHash != loaded.ContentHash || saved.Result.HTML != loaded.Result.HTML {
		t.Fatal(loaded, err)
	}
	name := filepath.Join(dir, p.Key()+".json")
	b, _ := os.ReadFile(name)
	for i := range b {
		if i+5 < len(b) && string(b[i:i+5]) == "Saved" {
			copy(b[i:i+5], "Oops!")
			break
		}
	}
	os.WriteFile(name, b, 0600)
	if _, err = s.Load(p); err == nil {
		t.Fatal("corruption not detected")
	}
}
func TestRejectedWritesPreserveSnapshot(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, 32)
	t.Cleanup(func() {
		if s != nil {
			s.Close()
		}
	}) // Windows cannot delete a folder still held open
	p := testPage(t)
	good := render.Result{HTML: "<h1>Original</h1>", Status: 200}
	if _, err := s.Save(p, good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []render.Result{{HTML: "error", Status: 500}, {HTML: "", Status: 200}, {HTML: string(make([]byte, 33)), Status: 200}} {
		if _, err := s.Save(p, bad); err == nil {
			t.Fatal("invalid write accepted")
		}
		got, err := s.Load(p)
		if err != nil || got.Result.HTML != good.HTML {
			t.Fatal("good snapshot lost", err)
		}
	}
	// A failed atomic rename must not leave a partial record or temporary file.
	other, _ := target.Map(p.Site, "http://127.0.0.1:8081", "/blocked")
	os.Mkdir(filepath.Join(dir, other.Key()+".json"), 0700)
	if _, err := s.Save(other, good); err == nil {
		t.Fatal("rename failure not returned")
	}
	temps, _ := filepath.Glob(filepath.Join(dir, ".snapshot-*.tmp"))
	if len(temps) != 0 {
		t.Fatal("temporary files leaked", temps)
	}
	if _, err := Open(filepath.Join(dir, p.Key()+".json", "child"), 32); err == nil {
		t.Fatal("invalid directory accepted")
	}
}
func TestConcurrentAtomicReplace(t *testing.T) {
	s, _ := Open(t.TempDir(), 1024)
	t.Cleanup(func() {
		if s != nil {
			s.Close()
		}
	}) // Windows cannot delete a folder still held open
	p := testPage(t)
	s.Save(p, render.Result{HTML: "<h1>initial</h1>", Status: 200})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 5 {
				if _, err := s.Save(p, render.Result{HTML: fmt.Sprintf("<h1>%d:%d</h1>", i, j), Status: 200}); err != nil {
					t.Error(err)
				}
				if _, err := s.Load(p); err != nil {
					t.Error("partial concurrent read", err)
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestOutcomesAndValidators(t *testing.T) {
	s, _ := Open(t.TempDir(), 1024)
	t.Cleanup(func() {
		if s != nil {
			s.Close()
		}
	}) // Windows cannot delete a folder still held open
	p := testPage(t)
	var previous string
	for _, r := range []render.Result{{Status: 404}, {Status: 410}, {Status: 301, Location: "https://elsewhere.example/"}, {Status: 200, HTML: "restored"}} {
		saved, err := s.Save(p, r)
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := s.Load(p)
		if err != nil || loaded.Result.Status != r.Status {
			t.Fatal(loaded, err)
		}
		if saved.ETag() == previous {
			t.Fatal("response semantics missing from validator")
		}
		previous = saved.ETag()
	}
	if _, err := s.Save(p, render.Result{Status: 200, HTML: "private", Headers: map[string]string{"Set-Cookie": "session=x"}}); err == nil {
		t.Fatal("private headers accepted")
	}
	if err := s.Delete(p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(p); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
