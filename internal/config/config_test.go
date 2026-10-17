package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidation(t *testing.T) {
	for _, origin := range []string{"", "ftp://example.com", "https://user:password@example.com", "https://example.com/app", "https://example.com?q=x", "https://example.com#x", "https://example.com:99999"} {
		t.Run(origin, func(t *testing.T) {
			c := Default()
			c.Origin = origin
			if c.Validate() == nil {
				t.Fatal("accepted invalid origin")
			}
		})
	}
	for _, origin := range []string{"https://example.com", "http://127.0.0.1:8081", "http://[::1]:8081/"} {
		c := Default()
		c.Origin = origin
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	c := Default()
	c.Origin = "https://example.com"
	c.AdminListen = ":9090"
	if c.Validate() == nil {
		t.Fatal("unprotected external admin accepted")
	}
	c.AdminToken = "secret"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.OriginTimeout = "-1s"
	if c.Validate() == nil {
		t.Fatal("negative timeout accepted")
	}
}
func TestLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	for _, body := range []string{`{"typo":true}`, `{} {}`, `null`, strings.Repeat(" ", 1<<20) + `{}`} {
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Errorf("accepted invalid configuration of length %d", len(body))
		}
	}
	if err := os.WriteFile(p, []byte(`{"origin":"https://example.com","bots":["MyCrawler"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || len(c.Bots) != 1 || c.Bots[0] != "MyCrawler" {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestRenderingConfigurationLimits(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.PublicURL = "https://example.com/subdir" },
		func(c *Config) { c.RenderAllowOrigins = []string{"file:///tmp"} },
		func(c *Config) { c.RenderTimeout = "0s" }, func(c *Config) { c.RenderSettle = "-1s" },
		func(c *Config) { c.MaxHTMLBytes = 0 }, func(c *Config) { c.MaxHTMLBytes = 65 << 20 },
		func(c *Config) { c.RenderConcurrency = 0 }, func(c *Config) { c.RenderConcurrency = 17 },
	} {
		c := Default()
		c.Origin = "https://example.com"
		mutate(&c)
		if c.Validate() == nil {
			t.Fatal("invalid render configuration accepted")
		}
	}
	c := Default()
	c.Origin = "http://127.0.0.1:8081"
	c.PublicURL = "https://public.example"
	c.RenderAllowOrigins = []string{"https://cdn.example"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSitemapAndWarmingLimits(t *testing.T) {
	for _, change := range []func(*Config){
		func(c *Config) { c.SitemapURLs = nil }, func(c *Config) { c.SitemapURLs = []string{"https://foreign.example/sitemap.xml"} },
		func(c *Config) { c.WarmQueueSize = 0 }, func(c *Config) { c.SitemapMaxURLs = 0 }, func(c *Config) { c.SitemapMaxDocuments = 0 }, func(c *Config) { c.SitemapMaxDepth = -1 },
		func(c *Config) { c.SitemapMaxBytes = 65 << 20 }, func(c *Config) { c.SitemapScanTimeout = "0s" }, func(c *Config) { c.WarmInterval = "invalid" },
		func(c *Config) { c.WarmRetryBase = "10s"; c.WarmRetryMax = "5s" },
	} {
		c := Default()
		c.Origin = "https://public.example"
		change(&c)
		if c.Validate() == nil {
			t.Fatal("invalid sitemap/warming configuration accepted")
		}
	}
	c := Default()
	c.Origin = "http://127.0.0.1:8081"
	c.PublicURL = "https://public.example"
	c.SitemapURLs = []string{"https://public.example/sitemap.xml"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotConfigurationLimits(t *testing.T) {
	for _, change := range []func(*Config){func(c *Config) { c.SnapshotTTL = "0s" }, func(c *Config) { c.RequestPageLimit = 0 }, func(c *Config) { c.RequestPageLimit = 10001 }} {
		c := Default()
		c.Origin = "https://example.com"
		change(&c)
		if c.Validate() == nil {
			t.Fatal("invalid snapshot configuration accepted")
		}
	}
}

func TestRoutingAndForwardingSettings(t *testing.T) {
	valid := Default()
	valid.Origin = "https://example.com"
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	prefixes, err := valid.TrustedPrefixes()
	if err != nil || len(prefixes) != 2 {
		t.Fatal(prefixes, err)
	}
	valid.TrustedProxies = []string{"10.0.0.7", "2001:db8::/32"}
	if prefixes, err := valid.TrustedPrefixes(); err != nil || prefixes[0].String() != "10.0.0.7/32" {
		t.Fatal(prefixes, err)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.TrustedProxies = []string{"not-an-ip"} },
		func(c *Config) { c.BypassPaths = []string{"api"} },
		func(c *Config) { c.BypassPaths = []string{"/"} },
		func(c *Config) { c.BypassPaths = []string{"/api?x"} },
		func(c *Config) { c.IgnoreQueryParams = []string{""} },
		func(c *Config) { c.IgnoreQueryParams = []string{"a=b"} },
	} {
		c := valid
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("accepted invalid routing settings: %+v %+v %+v", c.TrustedProxies, c.BypassPaths, c.IgnoreQueryParams)
		}
	}
	empty := valid
	empty.BypassPaths, empty.IgnoreQueryParams, empty.TrustedProxies = []string{}, []string{}, []string{}
	if err := empty.Validate(); err != nil {
		t.Fatal(err)
	}
}
