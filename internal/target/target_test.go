package target

import (
	"testing"
)

func TestMappingAndIdentity(t *testing.T) {
	p, err := Map("https://PUBLIC.example:443/", "http://127.0.0.1:8081/", "/a%2Fb?z=2&z=1#section")
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != "https://public.example/a%2Fb?z=2&z=1" || p.Origin != "http://127.0.0.1:8081/a%2Fb?z=2&z=1" {
		t.Fatalf("%+v", p)
	}
	if err = p.Validate(); err != nil {
		t.Fatal(err)
	}
	q, _ := Map("https://public.example", "http://127.0.0.1:8081", p.URL)
	if p.Key() != q.Key() {
		t.Fatal("equivalent identity changed")
	}
	q, _ = Map(p.Site, "http://127.0.0.1:8082", p.URL)
	if p.Key() == q.Key() {
		t.Fatal("origin not namespaced")
	}
	q, _ = Map(p.Site, "http://127.0.0.1:8081", "/a%2Fb?z=1&z=2")
	if p.Key() == q.Key() {
		t.Fatal("query order discarded")
	}
	final, err := p.PublicURL("http://127.0.0.1:8081/final?q=2")
	if err != nil || final != "https://public.example/final?q=2" {
		t.Fatal(final, err)
	}
}
func TestRejectForeignTargets(t *testing.T) {
	for _, raw := range []string{"https://other.example/", "//other.example/", "javascript:alert(1)", "file:///etc/passwd", "relative", "https://user@public.example/", "https://public.example:444/"} {
		if _, err := Map("https://public.example", "http://127.0.0.1:8081", raw); err == nil {
			t.Error("accepted", raw)
		}
	}
	p, _ := Map("https://public.example", "http://127.0.0.1:8081", "/")
	if _, err := p.PublicURL("https://elsewhere.example/"); err == nil {
		t.Fatal("foreign final URL accepted")
	}
	p.URL = "https://public.example:443/"
	if p.Validate() == nil {
		t.Fatal("noncanonical stored identity accepted")
	}
}
