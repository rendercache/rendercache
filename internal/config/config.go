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
	Origin             string   `json:"origin"`
	PublicURL          string   `json:"public_url"`
	DataDir            string   `json:"data_dir"`
	ChromePath         string   `json:"chrome_path"`
	ChromeNoSandbox    bool     `json:"chrome_no_sandbox"`
	RenderTimeout      string   `json:"render_timeout"`
	RenderSettle       string   `json:"render_settle"`
	ReadySelector      string   `json:"ready_selector"`
	MaxHTMLBytes       int64    `json:"max_html_bytes"`
	RenderConcurrency  int      `json:"render_concurrency"`
	RenderAllowOrigins []string `json:"render_allow_origins"`
	Listen             string   `json:"listen"`
	AdminListen        string   `json:"admin_listen"`
	AdminToken         string   `json:"admin_token,omitempty"`
	Bots               []string `json:"bots"`
	OriginTimeout      string   `json:"origin_timeout"`
	ShutdownTimeout    string   `json:"shutdown_timeout"`
}

func Default() Config {
	return Config{DataDir: "data/snapshots", RenderTimeout: "30s", RenderSettle: "500ms", MaxHTMLBytes: 8 << 20, RenderConcurrency: 1, Listen: ":8080", AdminListen: "127.0.0.1:9090", OriginTimeout: "30s", ShutdownTimeout: "10s", Bots: []string{
		"Googlebot", "bingbot", "DuckDuckBot", "OAI-SearchBot", "GPTBot", "ChatGPT-User", "ClaudeBot", "Claude-SearchBot", "Claude-User", "PerplexityBot", "Perplexity-User", "Meta-ExternalAgent", "Bytespider", "Applebot", "facebookexternalhit", "Twitterbot", "LinkedInBot", "Slackbot", "Discordbot",
	}}
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
