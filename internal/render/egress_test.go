package render

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEgressDestinationAndCredentials(t *testing.T) {
	var allowedCalls, forbiddenCalls atomic.Int32
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowedCalls.Add(1)
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("credentials forwarded")
		}
		io.WriteString(w, "allowed")
	}))
	defer allowed.Close()
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forbiddenCalls.Add(1) }))
	defer forbidden.Close()
	g, err := startGuard(context.Background(), []string{allowed.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	proxy, _ := url.Parse("http://" + g.address)
	tr := &http.Transport{Proxy: http.ProxyURL(proxy)}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: time.Second}
	for _, tc := range []struct {
		url, method string
		want        int
	}{{allowed.URL, "GET", 200}, {forbidden.URL, "GET", 403}, {allowed.URL, "POST", 403}} {
		req, _ := http.NewRequest(tc.method, tc.url, nil)
		req.Header.Set("Cookie", "secret=1")
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatal(resp.StatusCode)
		}
	}
	if allowedCalls.Load() != 1 || forbiddenCalls.Load() != 0 {
		t.Fatal("destination policy failed")
	}
}
func TestGuardConnectRestrictsTarget(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "tls origin") }))
	defer origin.Close()
	g, err := startGuard(context.Background(), []string{origin.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	// Use the test server's trusted client through the CONNECT guard.
	client := origin.Client()
	transport := client.Transport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	proxy, _ := url.Parse("http://" + g.address)
	transport.Proxy = http.ProxyURL(proxy)
	client.Transport = transport
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "tls origin" {
		t.Fatal(string(b))
	}
	conn, err := net.DialTimeout("tcp", g.address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	fmt.Fprint(conn, "CONNECT forbidden.example:443 HTTP/1.1\r\nHost: forbidden.example:443\r\n\r\n")
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.Contains(line, "403") {
		t.Fatal(line, err)
	}
}
