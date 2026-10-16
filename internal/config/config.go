// SPDX-License-Identifier: Apache-2.0
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/rendercache/rendercache/internal/target"
)

// Config is the on-disk JSON configuration. CLI flags override these values.
type Config struct {
	Origin              string   `json:"origin"`
	SnapshotEnabled     bool     `json:"snapshot_enabled"`
	SnapshotTTL         string   `json:"snapshot_ttl"`
	RequestPageLimit    int      `json:"request_page_limit"`
	WarmEnabled         bool     `json:"warm_enabled"`
	SitemapURLs         []string `json:"sitemap_urls"`
	WarmInterval        string   `json:"warm_interval"`
	SitemapInterval     string   `json:"sitemap_interval"`
	WarmRetryBase       string   `json:"warm_retry_base"`
	WarmRetryMax        string   `json:"warm_retry_max"`
	WarmQueueSize       int      `json:"warm_queue_size"`
	SitemapMaxBytes     int64    `json:"sitemap_max_bytes"`
	SitemapMaxURLs      int      `json:"sitemap_max_urls"`
	SitemapMaxDocuments int      `json:"sitemap_max_documents"`
	SitemapMaxDepth     int      `json:"sitemap_max_depth"`
	SitemapTimeout      string   `json:"sitemap_timeout"`
	SitemapScanTimeout  string   `json:"sitemap_scan_timeout"`
	PublicURL           string   `json:"public_url"`
	DataDir             string   `json:"data_dir"`
	ChromePath          string   `json:"chrome_path"`
	ChromeNoSandbox     bool     `json:"chrome_no_sandbox"`
	RenderTimeout       string   `json:"render_timeout"`
	RenderSettle        string   `json:"render_settle"`
	ReadySelector       string   `json:"ready_selector"`
	MaxHTMLBytes        int64    `json:"max_html_bytes"`
	RenderConcurrency   int      `json:"render_concurrency"`
	RenderAllowOrigins  []string `json:"render_allow_origins"`
	Listen              string   `json:"listen"`
	AdminListen         string   `json:"admin_listen"`
	AdminToken          string   `json:"admin_token,omitempty"`
	Bots                []string `json:"bots"`
	OriginTimeout       string   `json:"origin_timeout"`
	ShutdownTimeout     string   `json:"shutdown_timeout"`
}

func Default() Config {
	return Config{
		SnapshotEnabled: true, SnapshotTTL: "24h", RequestPageLimit: 256, Origin: "", PublicURL: "", Listen: ":8080", AdminListen: "127.0.0.1:9090",
		OriginTimeout: "30s", ShutdownTimeout: "10s",
		DataDir: "data/snapshots", RenderTimeout: "30s", RenderSettle: "500ms", MaxHTMLBytes: 8 << 20, RenderConcurrency: 1,
		WarmEnabled: true, SitemapURLs: []string{"/sitemap.xml"}, WarmInterval: "24h", SitemapInterval: "15m",
		WarmRetryBase: "5s", WarmRetryMax: "5m", WarmQueueSize: 128,
		SitemapMaxBytes: 8 << 20, SitemapMaxURLs: 10000, SitemapMaxDocuments: 128, SitemapMaxDepth: 4,
		SitemapTimeout: "15s", SitemapScanTimeout: "2m",
		Bots: []string{
			"Googlebot", "bingbot", "DuckDuckBot", "OAI-SearchBot", "GPTBot", "ChatGPT-User",
			"ClaudeBot", "Claude-SearchBot", "Claude-User", "PerplexityBot", "Perplexity-User",
			"Meta-ExternalAgent", "Bytespider", "Applebot", "facebookexternalhit", "Twitterbot", "LinkedInBot", "Slackbot", "Discordbot",
		},
	}
}

func Load(path string) (Config, error) {
	c := Default()
	if path == "" {
		return c, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return c, err
	}
	if info.Size() > 1<<20 {
		return c, errors.New("config exceeds 1 MiB")
	}
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	var raw json.RawMessage
	if err := d.Decode(&raw); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	if len(raw) == 0 || raw[0] != '{' {
		return c, errors.New("config must be a JSON object")
	}
	objectDecoder := json.NewDecoder(strings.NewReader(string(raw)))
	objectDecoder.DisallowUnknownFields()
	if err := objectDecoder.Decode(&c); err != nil {
		return c, fmt.Errorf("config: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, errors.New("config must contain one JSON object")
	}
	return c, nil
}

func (c Config) OriginURL() (*url.URL, error) {
	u, err := url.Parse(c.Origin)
	if err != nil {
		return nil, errors.New("origin must be an absolute http or https URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("origin must be an http(s) site root without credentials, query or fragment")
	}
	if p := u.Port(); p != "" {
		if _, err := net.LookupPort("tcp", p); err != nil {
			return nil, errors.New("invalid origin port")
		}
	}
	return u, nil
}

func (c Config) SiteURL() string {
	if c.PublicURL != "" {
		return c.PublicURL
	}
	return c.Origin
}

func (c Config) Validate() error {
	ttl, err := time.ParseDuration(c.SnapshotTTL)
	if err != nil || ttl < time.Second {
		return errors.New("snapshot_ttl must be at least 1s")
	}
	if c.RequestPageLimit < 1 || c.RequestPageLimit > 10000 {
		return errors.New("request_page_limit must be between 1 and 10000")
	}
	if _, err := target.Root(c.SiteURL()); err != nil {
		return fmt.Errorf("public_url: %w", err)
	}
	if c.DataDir == "" || c.MaxHTMLBytes <= 0 || c.MaxHTMLBytes > 64<<20 || c.RenderConcurrency < 1 || c.RenderConcurrency > 16 {
		return errors.New("invalid rendering/storage limits")
	}
	for _, raw := range c.RenderAllowOrigins {
		if _, err := target.Root(raw); err != nil {
			return fmt.Errorf("render_allow_origins: %w", err)
		}
	}
	settle, err := time.ParseDuration(c.RenderSettle)
	if err != nil || settle < 0 {
		return errors.New("render_settle must be nonnegative")
	}

	if c.WarmQueueSize < 1 || c.WarmQueueSize > 10000 || c.SitemapMaxBytes < 1 || c.SitemapMaxBytes > 64<<20 || c.SitemapMaxURLs < 1 || c.SitemapMaxURLs > 100000 || c.SitemapMaxDocuments < 1 || c.SitemapMaxDocuments > 1000 || c.SitemapMaxDepth < 0 || c.SitemapMaxDepth > 16 || len(c.SitemapURLs) == 0 {
		return errors.New("invalid warming/sitemap limits")
	}
	for _, raw := range c.SitemapURLs {
		if _, err := target.Map(c.SiteURL(), c.Origin, raw); err != nil {
			return fmt.Errorf("sitemap_urls: %w", err)
		}
	}
	for _, value := range []string{c.WarmInterval, c.SitemapInterval, c.SitemapTimeout, c.SitemapScanTimeout, c.WarmRetryBase, c.WarmRetryMax} {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid warming duration %q", value)
		}
	}
	warmInterval, _ := time.ParseDuration(c.WarmInterval)
	sitemapInterval, _ := time.ParseDuration(c.SitemapInterval)
	if warmInterval < time.Second || sitemapInterval < time.Second {
		return errors.New("warming/discovery intervals must be at least 1s")
	}
	retryBase, _ := time.ParseDuration(c.WarmRetryBase)
	retryMax, _ := time.ParseDuration(c.WarmRetryMax)
	if retryBase < 100*time.Millisecond {
		return errors.New("warm_retry_base must be at least 100ms")
	}
	if retryMax < retryBase {
		return errors.New("warm_retry_max must not be less than warm_retry_base")
	}

	if _, err := c.OriginURL(); err != nil {
		return err
	}
	for _, address := range []string{c.Listen, c.AdminListen} {
		if _, _, err := net.SplitHostPort(address); err != nil {
			return fmt.Errorf("invalid listen address %q: %w", address, err)
		}
	}
	host, _, _ := net.SplitHostPort(c.AdminListen)
	ip := net.ParseIP(host)
	if (ip == nil || !ip.IsLoopback()) && strings.TrimSpace(c.AdminToken) == "" {
		return errors.New("admin_token is required when admin_listen is not a loopback IP")
	}
	for _, value := range []string{c.OriginTimeout, c.ShutdownTimeout, c.RenderTimeout} {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid positive duration %q", value)
		}
	}
	for _, bot := range c.Bots {
		if strings.TrimSpace(bot) == "" {
			return errors.New("bot patterns cannot be empty")
		}
	}
	return nil
}
