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
