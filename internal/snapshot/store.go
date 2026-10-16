// SPDX-License-Identifier: Apache-2.0
// Package snapshot stores each HTML document and its metadata as one atomically
// replaced, checksum-verified record. No database or sidecar-file consistency is required.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
type Store struct {
	dir     string
	maxHTML int64
}

func Open(dir string, maxHTML int64) (*Store, error) {
	if dir == "" || maxHTML <= 0 || maxHTML > 64<<20 {
		return nil, errors.New("invalid snapshot store limits")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &Store{dir: dir, maxHTML: maxHTML}, nil
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
		return Record{}, errors.New("only nonempty, successful HTML within size limit can be saved")
	}
	record := Record{Version: 1, Key: p.Key(), Page: p, CreatedAt: time.Now().UTC(), ContentHash: hash(result.HTML), Result: result}
	f, err := os.CreateTemp(s.dir, ".snapshot-*.tmp")
	if err != nil {
		return Record{}, err
	}
	name := f.Name()
	defer os.Remove(name)
	encoder := json.NewEncoder(f)
	encoder.SetEscapeHTML(false)
	err = encoder.Encode(record)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return Record{}, err
	}
	if err = os.Rename(name, filepath.Join(s.dir, record.Key+".json")); err != nil {
		return Record{}, err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return record, fmt.Errorf("snapshot installed but directory open failed: %w", err)
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return record, fmt.Errorf("snapshot installed but directory sync failed: %w", err)
	}
	return record, nil
}
func (s *Store) Load(p target.Page) (Record, error) {
	var record Record
	if err := p.Validate(); err != nil {
		return record, err
	}
	f, err := os.Open(filepath.Join(s.dir, p.Key()+".json"))
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
	if info.Size() > limit {
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
	err := os.Remove(filepath.Join(s.dir, p.Key()+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
