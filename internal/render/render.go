// SPDX-License-Identifier: Apache-2.0
package render

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/rendercache/rendercache/internal/target"
)

type Redirect struct {
	URL      string `json:"url"`
	Status   int64  `json:"status"`
	Location string `json:"location,omitempty"`
}
type Result struct {
	Headers         map[string]string `json:"headers,omitempty"`
	HTML            string            `json:"html"`
	Status          int               `json:"status"`
	Location        string            `json:"location,omitempty"`
	FinalURL        string            `json:"final_url"`
	Title           string            `json:"title"`
	Canonical       string            `json:"canonical,omitempty"`
	Robots          string            `json:"robots,omitempty"`
	Renderer        string            `json:"renderer"`
	Duration        time.Duration     `json:"duration"`
	Redirects       []Redirect        `json:"redirects,omitempty"`
	BlockedRequests uint64            `json:"blocked_requests"`
}
type Engine interface {
	Render(context.Context, target.Page) (Result, error)
}

// ValidateOutcome permits public HTML, same-site redirects and confirmed removals.
func ValidateOutcome(p target.Page, r Result) error {
	for k, v := range r.Headers {
		if privateHeader(k, v) {
			return ErrPrivate
		}
	}
	switch r.Status {
	case 200:
		if r.HTML == "" || r.Location != "" || len(r.Redirects) > 0 {
			return errors.New("invalid HTML outcome or legacy followed redirect")
		}
	case 301, 302, 303, 307, 308:
		u, err := url.Parse(r.Location)
		if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || strings.ContainsAny(r.Location, "\r\n") {
			return errors.New("invalid redirect location")
		}

		if r.HTML != "" {
			return errors.New("redirect must not retain destination HTML")
		}
	case 404, 410:
		if r.HTML != "" || r.Location != "" {
			return errors.New("invalid removal outcome")
		}
	default:
		return errors.New("uncacheable HTTP status")
	}
	return nil
}

var ErrPrivate = errors.New("response is ineligible for shared snapshots")

func redirectLocation(p target.Page, from, location string) (string, error) {
	base, err := url.Parse(from)
	if err != nil {
		return "", err
	}
	u, err := base.Parse(location)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || strings.ContainsAny(location, "\r\n") {
		return "", errors.New("invalid redirect location")
	}
	origin, _ := url.Parse(p.Origin)
	if u.Scheme == origin.Scheme && u.Host == origin.Host {
		mapped, err := p.PublicURL(u.String())
		if err != nil {
			return "", err
		}
		if u.Fragment != "" {
			mapped += "#" + u.EscapedFragment()
		}
		return mapped, nil
	}
	return u.String(), nil
}
