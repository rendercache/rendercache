// SPDX-License-Identifier: Apache-2.0
package proxy

import (
	"net/http/httptest"
	"testing"

	"github.com/rendercache/rendercache/internal/config"
)

// Review of 0.3 Phase A: each case failed on 0951c92.

// A reload that drops the admin token while the admin listener, which needs a
// restart to move, is still public must be refused.
func TestReview030ReloadKeepsAdminTokenOnPublicListener(t *testing.T) {
	g, _, _ := verifiedGateway(t)
	g.edit(func(c *config.Config) { c.AdminListen = "0.0.0.0:9090"; c.AdminToken = "secret" })
	next := g.Settings()
	next.AdminListen = "127.0.0.1:9090"
	next.AdminToken = ""
	if _, err := g.Apply(next); err == nil {
		t.Fatal("reload removed the token of a public admin listener")
	}
	w := httptest.NewRecorder()
	g.AdminHandler().ServeHTTP(w, httptest.NewRequest("GET", "http://admin.test/status", nil))
	if w.Code != 401 {
		t.Fatalf("admin open without a token: %d", w.Code)
	}
}

// Verification follows the crawler the user agent names, not the wording of
// the bots patterns that matched it.
func TestReview030CustomPatternsStillVerify(t *testing.T) {
	g, _, _ := verifiedGateway(t)
	const gpt = "Mozilla/5.0 (compatible; GPTBot/1.2; +https://openai.com/gptbot)"
	for _, bots := range [][]string{{"GPTBot/"}, {"bot"}, {"gptbot"}, {"compatible"}} {
		g.edit(func(c *config.Config) { c.Bots = bots })
		if got := fetch(g, gpt, "198.51.100.7:4000").Header().Get("X-RenderCache"); got != "BYPASS" {
			t.Errorf("bots %q: spoofed GPTBot got %s", bots, got)
		}
		if got := fetch(g, gpt, "203.0.113.9:4000").Header().Get("X-RenderCache"); got != "HIT" {
			t.Errorf("bots %q: real GPTBot got %s", bots, got)
		}
	}
}

// Proxies that write ports into X-Forwarded-For still identify the client.
func TestReview030ForwardedForWithPorts(t *testing.T) {
	g, _, _ := verifiedGateway(t)
	for _, xff := range []string{"203.0.113.9:51234", "[2001:db8:4::7]:443", "unknown, 203.0.113.9"} {
		if got := fetch(g, "GPTBot", "10.1.2.3:4000", xff).Header().Get("X-RenderCache"); got != "HIT" {
			t.Errorf("%q: %s", xff, got)
		}
	}
	if got := fetch(g, "GPTBot", "10.1.2.3:4000", "203.0.113.9, unknown").Header().Get("X-RenderCache"); got != "BYPASS" {
		t.Errorf("unknown nearest hop verified: %s", got)
	}
}
