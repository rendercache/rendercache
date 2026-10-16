// SPDX-License-Identifier: Apache-2.0
package render

import (
	"context"
	"time"

	"github.com/rendercache/rendercache/internal/target"
)

type Redirect struct {
	URL    string `json:"url"`
	Status int64  `json:"status"`
}
type Result struct {
	Headers         map[string]string `json:"headers,omitempty"`
	HTML            string            `json:"html"`
	Status          int               `json:"status"`
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
