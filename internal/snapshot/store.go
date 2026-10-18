// SPDX-License-Identifier: Apache-2.0
// Package snapshot stores each HTML document and its metadata as one atomically
// replaced, checksum-verified record. No database or sidecar-file consistency is required.
package snapshot

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rendercache/rendercache/internal/localfs"
	"io"
	"os"
	"sync"
	"time"

	"github.com/rendercache/rendercache/internal/render"
	"github.com/rendercache/rendercache/internal/target"
)

var ErrNotFound = errors.New("snapshot not found")

type Record struct {
	Version     int           `json:"version"`
	Key         string        `json:"key"`
	Page        target.Page   `json:"page"`
	CreatedAt   time.Time     `json:"created_at"`
	ContentHash string        `json:"content_hash"`
	Result      render.Result `json:"result"`
}
type Limits struct {
	MaxBytes int64
	MaxFiles int
}

var DefaultLimits = Limits{MaxBytes: 1 << 30, MaxFiles: 10000}
var ErrQuota = errors.New("snapshot quota exceeded; prune old records or increase limits")

type fileEntry struct {
	Size int64
}
type Status struct {
	Bytes     int64  `json:"bytes"`
	Files     int    `json:"files"`
	MaxBytes  int64  `json:"max_bytes"`
	MaxFiles  int    `json:"max_files"`
	OverLimit bool   `json:"over_limit"`
	Rejected  uint64 `json:"rejected_writes"`
}
type Store struct {
	root     *os.Root
	maxHTML  int64
	limits   Limits
	mu       sync.Mutex
	files    map[string]fileEntry
	bytes    int64
	rejected uint64
}

func Open(dir string, maxHTML int64) (*Store, error) {
	return OpenWithLimits(dir, maxHTML, DefaultLimits)
}
func OpenWithLimits(dir string, maxHTML int64, limits Limits) (*Store, error) {
	return openStore(dir, maxHTML, limits, true)
}

// OpenExisting never creates a missing directory; used by the read-only inspect command.
func OpenExisting(dir string, maxHTML int64, limits Limits) (*Store, error) {
	return openStore(dir, maxHTML, limits, false)
}
func openStore(dir string, maxHTML int64, limits Limits, create bool) (*Store, error) {
	if dir == "" || maxHTML <= 0 || maxHTML > 64<<20 || limits.MaxBytes < 4096 || limits.MaxBytes > 1<<40 || limits.MaxFiles < 1 || limits.MaxFiles > 100000 {
		return nil, errors.New("invalid snapshot store limits")
	}
	if create {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{root: root, maxHTML: maxHTML, limits: limits, files: make(map[string]fileEntry)}
	if err := s.scan(); err != nil {
		root.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.root.Close() }
func (s *Store) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{Bytes: s.bytes, Files: len(s.files), MaxBytes: s.limits.MaxBytes, MaxFiles: s.limits.MaxFiles, OverLimit: s.bytes > s.limits.MaxBytes || len(s.files) > s.limits.MaxFiles, Rejected: s.rejected}
}
func recordName(name string) bool {
	if len(name) != 69 || name[64:] != ".json" {
		return false
	}
	_, err := hex.DecodeString(name[:64])
	return err == nil && name == string(bytes.ToLower([]byte(name)))
}
func (s *Store) scan() error {
	dir, err := s.root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	count := 0
	for {
		entries, err := dir.ReadDir(256)
		if err != nil && err != io.EOF {
			return err
		}
		for _, e := range entries {
			count++
			if count > 200000 {
				return errors.New("snapshot directory exceeds scan limit")
			}
			if !recordName(e.Name()) {
				continue
			}
			info, err := s.root.Lstat(e.Name())
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("snapshot filename is not a regular file")
			}
			if len(s.files) >= 100000 {
				return errors.New("snapshot file count exceeds scan limit")
			}
			s.files[e.Name()] = fileEntry{Size: info.Size()}
			s.bytes += info.Size()
		}
		if err == io.EOF {
			return nil
		}
	}
}
func hash(html string) string { sum := sha256.Sum256([]byte(html)); return hex.EncodeToString(sum[:]) }
func (s *Store) Save(p target.Page, result render.Result) (Record, error) {
	if err := p.Validate(); err != nil {
		return Record{}, err
	}
	if err := render.ValidateOutcome(p, result); err != nil {
		return Record{}, err
	}
	if int64(len(result.HTML)) > s.maxHTML {
		return Record{}, errors.New("snapshot HTML exceeds size limit")
	}
	record := Record{Version: 1, Key: p.Key(), Page: p, CreatedAt: time.Now().UTC(), ContentHash: hash(result.HTML), Result: result}

	// Serialize before admission; a failed encoding/quota check cannot disturb an
	// existing snapshot. Only one temporary record is written at a time per store.
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return Record{}, err
	}
	if int64(payload.Len()) > s.maxHTML*6+(1<<20) {
		return Record{}, errors.New("snapshot metadata exceeds size limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := record.Key + ".json"
	previous := s.files[key]
	count := len(s.files)
	if _, exists := s.files[key]; !exists {
		count++
	}
	nextBytes := s.bytes - previous.Size + int64(payload.Len())
	// Allow a shrinking refresh when an administrator lowers limits below usage.
	if (nextBytes > s.limits.MaxBytes && nextBytes > s.bytes) || (count > s.limits.MaxFiles && count > len(s.files)) {
		s.rejected++
		return Record{}, ErrQuota
	}
	name := ".snapshot-" + rand.Text() + ".tmp"
	f, err := s.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Record{}, err
	}

	defer s.root.Remove(name)
	if _, err = f.Write(payload.Bytes()); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return Record{}, err
	}
	if err = localfs.Rename(s.root, name, key); err != nil {
		return Record{}, err
	}
	s.bytes = nextBytes
	s.files[key] = fileEntry{Size: int64(payload.Len())}
	if err = s.sync(); err != nil {
		return record, fmt.Errorf("snapshot installed but directory sync failed: %w", err)
	}

	return record, nil
}
func (s *Store) Load(p target.Page) (Record, error) {
	var record Record
	if err := p.Validate(); err != nil {
		return record, err
	}
	f, err := localfs.OpenRegular(s.root, p.Key()+".json", os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return record, ErrNotFound
	}
	if err != nil {
		return record, err
	}
	defer f.Close()
	limit := s.maxHTML*6 + (1 << 20)
	info, err := f.Stat()
	if err != nil {
		return record, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return record, errors.New("snapshot record exceeds size limit")
	}
	decoder := json.NewDecoder(io.LimitReader(f, limit))
	if err = decoder.Decode(&record); err != nil {
		return record, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return record, errors.New("trailing snapshot data")
	}
	if record.Version != 1 || record.Key != p.Key() || record.Page != p || record.CreatedAt.IsZero() || render.ValidateOutcome(p, record.Result) != nil || int64(len(record.Result.HTML)) > s.maxHTML || record.ContentHash != hash(record.Result.HTML) {
		return Record{}, errors.New("invalid snapshot identity or checksum")
	}
	return record, nil
}

// ETag covers response status, location and safe metadata as well as body bytes.
func (r Record) ETag() string {
	b, _ := json.Marshal(struct {
		Status   int
		Location string
		Hash     string
		Headers  map[string]string
	}{r.Result.Status, r.Result.Location, r.ContentHash, r.Result.Headers})
	sum := sha256.Sum256(b)
	return `"rc-` + hex.EncodeToString(sum[:]) + `"`
}

func (s *Store) Delete(p target.Page) error {
	if err := p.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remove(p.Key() + ".json")
}
func (s *Store) sync() error {
	return localfs.SyncDir(s.root)
}
func (s *Store) remove(name string) error {
	info, err := s.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		if old, ok := s.files[name]; ok {
			s.bytes -= old.Size
			delete(s.files, name)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("refusing to remove a non-regular snapshot file")
	}
	if err = s.root.Remove(name); err != nil {
		return err
	}
	if old, ok := s.files[name]; ok {
		s.bytes -= old.Size
		delete(s.files, name)
	}
	return s.sync()
}

// Prune removes only recognized regular snapshot records older than cutoff.
// It deliberately leaves delivery logs, lock files, temp files and unknown files.
// Files with mismatching identities/corruption are still accounted by filename
// and can be pruned by age. No browser work or implicit eviction is performed.
func (s *Store) Prune(cutoff time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for name := range s.files {
		info, err := s.root.Lstat(name)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				if e, ok := s.files[name]; ok {
					s.bytes -= e.Size
					delete(s.files, name)
				}
				continue
			}
			return removed, err
		}
		if !info.Mode().IsRegular() {
			return removed, errors.New("refusing to prune a non-regular snapshot file")
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := s.remove(name); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// CleanupTemps is called only by commands holding the data-directory lock.
// A crashed process cannot hold that lock. Inspect never calls this method.
func (s *Store) CleanupTemps() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.root.Open(".")
	if err != nil {
		return 0, err
	}
	defer dir.Close()
	removed, count := 0, 0
	for {
		entries, err := dir.ReadDir(256)
		if err != nil && err != io.EOF {
			return removed, err
		}
		for _, e := range entries {
			count++
			if count > 200000 {
				return removed, errors.New("snapshot directory exceeds scan limit")
			}
			name := e.Name()
			if !tempName(name) {
				continue
			}
			info, err := s.root.Lstat(name)
			if err != nil {
				return removed, err
			}
			if !info.Mode().IsRegular() {
				return removed, errors.New("temporary snapshot name is not a regular file")
			}
			if err = s.root.Remove(name); err != nil {
				return removed, err
			}
			removed++
		}
		if err == io.EOF {
			if removed > 0 {
				return removed, s.sync()
			}
			return removed, nil
		}
	}
}
func tempName(name string) bool {
	if len(name) != 40 || name[:10] != ".snapshot-" || name[36:] != ".tmp" {
		return false
	}
	for _, r := range name[10:36] {
		if !(r >= 'A' && r <= 'Z' || r >= '2' && r <= '7') {
			return false
		}
	}
	return true
}
