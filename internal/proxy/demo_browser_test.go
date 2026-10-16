package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/rendercache/rendercache/internal/render"
)

func TestDemoRealBrowser(t *testing.T) {
	chrome := os.Getenv("RENDERCACHE_TEST_CHROME")
	if chrome == "" {
		t.Skip("set RENDERCACHE_TEST_CHROME for browser UI check")
	}
	noSandbox := os.Getenv("RENDERCACHE_TEST_NO_SANDBOX") == "1"
	g, l := demoGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("admin authorization forwarded to origin")
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<!doctype html><html><body><main></main><script>window.uiPwned=true;document.querySelector('main').innerHTML='<h1 id="ready">A public page worth finding</h1><p>Useful article content that search crawlers should see without having to run JavaScript.</p><p>&lt;img src=x onerror=alert(1)&gt;</p>';</script></body></html>`)
	}))
	engine, err := render.NewChrome(render.Options{ChromePath: chrome, NoSandbox: noSandbox, Timeout: 10 * time.Second, ReadySelector: "#ready", Settle: 10 * time.Millisecond, MaxHTMLBytes: 1 << 20, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	result, err := engine.Render(ctx, mapped(g, "/"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = g.store.Save(mapped(g, "/"), result); err != nil {
		t.Fatal(err)
	}
	admin := httptest.NewServer(g.AdminHandler())
	defer admin.Close()
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(chrome), chromedp.Flag("no-sandbox", noSandbox))
	alloc, closeAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer closeAlloc()
	tab, closeTab := chromedp.NewContext(alloc)
	defer closeTab()
	if err = chromedp.Do(tab); err != nil {
		t.Fatal(err)
	}
	if _, err = chromedp.Call(tab, network.Enable, network.EnableParams{}); err != nil {
		t.Fatal(err)
	}
	if _, err = chromedp.Call(tab, network.SetExtraHTTPHeaders, network.SetExtraHTTPHeadersParams{Headers: network.Headers{"Authorization": "Bearer demo-secret"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = chromedp.Call(tab, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{Width: 1280, Height: 1100, DeviceScaleFactor: 1, Mobile: false}); err != nil {
		t.Fatal(err)
	}
	if _, err = chromedp.Run(tab, chromedp.NavigateResponse(admin.URL)); err != nil {
		t.Fatal(err)
	}
	poll := func(js string) {
		t.Helper()
		if _, e := chromedp.Run(tab, chromedp.Poll[bool](js, chromedp.WithPollingInterval(20*time.Millisecond))); e != nil {
			t.Fatal(e)
		}
	}
	poll(`document.getElementById('status-message').textContent.includes('Origin:')`)
	if _, err = chromedp.Run(tab, chromedp.Evaluate[any](`document.getElementById('compare').click()`)); err != nil {
		t.Fatal(err)
	}
	poll(`document.getElementById('result-message').textContent.includes('Likely JavaScript shell')`)
	safe, err := chromedp.Run(tab, chromedp.Evaluate[bool](`!window.uiPwned && document.querySelectorAll('#rendered-text mark').length>=10 && document.getElementById('raw-html').textContent.includes('<script>') && document.getElementById('snapshot-html').textContent.includes('Useful article content') && !document.querySelector('#raw-html script') && !document.querySelector('#snapshot-html img')`))
	if err != nil || !safe {
		t.Fatal("unsafe or incomplete comparison UI", safe, err)
	}
	if _, err = chromedp.Run(tab, chromedp.Evaluate[any](`document.getElementById('simulate').click()`)); err != nil {
		t.Fatal(err)
	}
	poll(`document.getElementById('logs').textContent.includes('SIMULATED') && document.getElementById('simulation').textContent.includes('Local simulation only')`)
	events := l.Status().Recent
	if len(events) != 1 || !events[0].Simulated || !events[0].FullHTML {
		t.Fatal(events)
	}
	if path := os.Getenv("RENDERCACHE_TEST_SCREENSHOT"); path != "" {
		shot, e := chromedp.Call(tab, page.CaptureScreenshot, page.CaptureScreenshotParams{})
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, shot.Data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	// Exercise the responsive layout as well as the desktop route.
	if _, err = chromedp.Call(tab, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{Width: 390, Height: 844, DeviceScaleFactor: 1, Mobile: true}); err != nil {
		t.Fatal(err)
	}
	fits, err := chromedp.Run(tab, chromedp.Evaluate[bool](`document.documentElement.scrollWidth<=window.innerWidth`))
	if err != nil || !fits {
		t.Fatal("mobile overflow", err)
	}
	if path := os.Getenv("RENDERCACHE_TEST_SCREENSHOT"); path != "" {
		shot, e := chromedp.Call(tab, page.CaptureScreenshot, page.CaptureScreenshotParams{})
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(strings.TrimSuffix(path, ".png")+"-mobile.png", shot.Data, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
