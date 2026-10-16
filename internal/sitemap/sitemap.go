// SPDX-License-Identifier: Apache-2.0
// Package sitemap discovers a bounded public page set, fetching only from the
// configured origin even when sitemap loc elements use the public hostname.
package sitemap

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rendercache/rendercache/internal/target"
)

type Options struct {
	Site         string
	Origin       string
	URLs         []string
	MaxBytes     int64
	MaxURLs      int
	MaxDocuments int
	MaxDepth     int
	Timeout      time.Duration
	ScanTimeout  time.Duration
}
type Source struct {
	opts      Options
	client    *http.Client
	transport *http.Transport
}

func New(o Options) (*Source, error) {
	if o.ScanTimeout == 0 {
		o.ScanTimeout = 2 * time.Minute
	}
	if o.ScanTimeout < 0 {
		return nil, errors.New("invalid sitemap scan timeout")
	}
	if _, err := target.Root(o.Site); err != nil {
		return nil, err
	}
	if _, err := target.Root(o.Origin); err != nil {
		return nil, err
	}
	if len(o.URLs) == 0 || o.MaxBytes < 1 || o.MaxURLs < 1 || o.MaxDocuments < 1 || o.MaxDepth < 0 || o.Timeout <= 0 {
		return nil, errors.New("invalid sitemap limits")
	}
	for _, raw := range o.URLs {
		if _, err := target.Map(o.Site, o.Origin, raw); err != nil {
			return nil, fmt.Errorf("sitemap URL: %w", err)
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableCompression = true
	s := &Source{opts: o, transport: tr}
	s.client = &http.Client{Transport: tr, Timeout: o.Timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("sitemap redirect limit reached")
		}
		p, _ := target.Map(o.Site, o.Origin, "/")
		if _, err := p.PublicURL(req.URL.String()); err != nil {
			return errors.New("sitemap redirect left configured origin")
		}
		return nil
	}}
	return s, nil
}
func (s *Source) Close() { s.transport.CloseIdleConnections() }

type item struct {
	page  target.Page
	depth int
}

func (s *Source) Discover(ctx context.Context) ([]target.Page, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.ScanTimeout)
	defer cancel()
	todo := make([]item, 0, len(s.opts.URLs))
	seenDocs := map[string]bool{}
	seenPages := map[string]bool{}
	pages := []target.Page{}
	addDoc := func(raw string, depth int) error {
		p, err := target.Map(s.opts.Site, s.opts.Origin, raw)
		if err != nil {
			return fmt.Errorf("foreign/invalid sitemap location: %w", err)
		}
		if seenDocs[p.Key()] {
			return nil
		}
		if depth > s.opts.MaxDepth {
			return errors.New("sitemap nesting limit exceeded")
		}
		if len(seenDocs) >= s.opts.MaxDocuments {
			return errors.New("sitemap document limit exceeded")
		}
		seenDocs[p.Key()] = true
		todo = append(todo, item{p, depth})
		return nil
	}
	for _, raw := range s.opts.URLs {
		if err := addDoc(raw, 0); err != nil {
			return nil, err
		}
	}
	for len(todo) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := todo[0]
		todo = todo[1:]
		data, err := s.fetch(ctx, current.page)
		if err != nil {
			return nil, err
		}
		err = parse(ctx, data, func(kind, loc string) error {
			if kind == "sitemapindex" {
				return addDoc(loc, current.depth+1)
			}
			// A foreign or malformed page loc invalidates the scan, rather than silently
			// overstating coverage. Never fetch or render it.
			p, err := target.Map(s.opts.Site, s.opts.Origin, loc)
			if err != nil {
				return fmt.Errorf("foreign/invalid page location: %w", err)
			}
			if seenPages[p.Key()] {
				return nil
			}
			if len(pages) >= s.opts.MaxURLs {
				return errors.New("sitemap page limit exceeded")
			}
			seenPages[p.Key()] = true
			pages = append(pages, p)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("parse sitemap: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return pages, nil
}
func (s *Source) fetch(ctx context.Context, p target.Page) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Origin, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "RenderCache-Sitemap/0.1")
	req.Header.Set("Accept", "application/xml,text/xml,application/gzip")
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch sitemap: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sitemap returned HTTP %d", resp.StatusCode)
	}
	wire, err := readBounded(resp.Body, s.opts.MaxBytes)
	if err != nil {
		return nil, err
	}
	if len(wire) >= 2 && wire[0] == 0x1f && wire[1] == 0x8b {
		decoder, err := gzip.NewReader(bytes.NewReader(wire))
		if err != nil {
			return nil, err
		}
		defer decoder.Close()
		return readBounded(decoder, s.opts.MaxBytes)
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return nil, errors.New("unsupported or invalid sitemap content encoding")
	}
	return wire, nil
}
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("sitemap byte limit exceeded")
	}
	return b, nil
}

func parse(ctx context.Context, data []byte, visit func(string, string) error) error {
	decoder := xml.NewDecoder(contextReader{ctx, bytes.NewReader(data)})
	root := ""
	depth := 0
	finished := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if root == "" || !finished {
				return errors.New("incomplete sitemap XML")
			}
			return nil
		}
		if err != nil {
			return err
		}
		switch t := token.(type) {
		case xml.Directive:
			return errors.New("XML directives are not accepted in sitemaps")
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(t)) != "" {
				return errors.New("text outside sitemap root")
			}
		case xml.StartElement:
			if finished {
				return errors.New("multiple sitemap roots")
			}
			depth++
			if depth == 1 {
				root = t.Name.Local
				if root != "urlset" && root != "sitemapindex" {
					return errors.New("unsupported sitemap root")
				}
				if t.Name.Space != "" && t.Name.Space != "http://www.sitemaps.org/schemas/sitemap/0.9" {
					return errors.New("unsupported sitemap namespace")
				}
				continue
			}
			expected := "url"
			if root == "sitemapindex" {
				expected = "sitemap"
			}
			if depth == 2 && t.Name.Local == expected {
				var entry struct {
					Locations []string `xml:"loc"`
				}
				if err := decoder.DecodeElement(&entry, &t); err != nil {
					return err
				}
				depth--
				if len(entry.Locations) != 1 || strings.TrimSpace(entry.Locations[0]) == "" {
					return errors.New("sitemap entry needs exactly one location")
				}
				loc := strings.TrimSpace(entry.Locations[0])
				if len(loc) > 8192 {
					return errors.New("sitemap location exceeds 8192 bytes")
				}
				u, err := url.Parse(loc)
				if err != nil || !u.IsAbs() {
					return errors.New("sitemap loc must be an absolute URL")
				}
				if err := visit(root, loc); err != nil {
					return err
				}
			} else {
				if err := decoder.Skip(); err != nil {
					return err
				}
				depth--
			}
		case xml.EndElement:
			depth--
			if depth == 0 {
				finished = true
			}
		}
	}
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
