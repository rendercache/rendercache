// SPDX-License-Identifier: Apache-2.0
package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/rendercache/rendercache/internal/target"
)

type Options struct {
	ChromePath     string
	NoSandbox      bool
	Timeout        time.Duration
	Settle         time.Duration
	ReadySelector  string
	MaxHTMLBytes   int64
	AllowedOrigins []string
	Concurrency    int
}
type Chrome struct {
	opts  Options
	slots chan struct{}
}

func NewChrome(o Options) (*Chrome, error) {
	if o.Timeout <= 0 || o.Settle < 0 || o.MaxHTMLBytes <= 0 || o.MaxHTMLBytes > 64<<20 || o.Concurrency < 1 || o.Concurrency > 16 {
		return nil, errors.New("invalid Chrome limits")
	}
	for _, origin := range o.AllowedOrigins {
		if _, err := target.Root(origin); err != nil {
			return nil, err
		}
	}
	return &Chrome{opts: o, slots: make(chan struct{}, o.Concurrency)}, nil
}
func (c *Chrome) Render(parent context.Context, p target.Page) (Result, error) {
	var result Result
	if err := p.Validate(); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(parent, c.opts.Timeout)
	defer cancel()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return result, ctx.Err()
	}
	started := time.Now()
	origin, _ := url.Parse(p.Origin)
	originRoot := origin.Scheme + "://" + origin.Host
	origins := append([]string{originRoot}, c.opts.AllowedOrigins...)
	guard, err := startGuard(ctx, origins)
	if err != nil {
		return result, err
	}
	defer guard.Close()
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.Flag("no-sandbox", c.opts.NoSandbox), chromedp.ProxyServer("http://"+guard.address), chromedp.Flag("proxy-bypass-list", "<-loopback>"), chromedp.Flag("disable-quic", true), chromedp.Flag("disable-background-networking", true), chromedp.Flag("disable-features", "WebRtcHideLocalIpsWithMdns"), chromedp.Flag("force-webrtc-ip-handling-policy", "disable_non_proxied_udp"))
	if c.opts.ChromePath != "" {
		opts = append(opts, chromedp.ExecPath(c.opts.ChromePath))
	}
	alloc, closeAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer closeAlloc()
	tab, closeTab := chromedp.NewContext(alloc)
	defer closeTab()
	if err := chromedp.Do(tab); err != nil {
		return result, fmt.Errorf("start Chrome: %w", err)
	}
	version, err := chromedp.CallBrowser(tab, browser.GetVersion, cdp.Empty{})
	if err != nil {
		return result, err
	}
	result.Renderer = version.Product
	frameTree, err := chromedp.Call(tab, page.GetFrameTree, cdp.Empty{})
	if err != nil {
		return result, err
	}
	mainFrame := frameTree.FrameTree.Frame.ID
	eventsCtx, stopEvents := context.WithCancel(tab)
	paused := chromedp.Events(eventsCtx, fetch.RequestPaused)
	extraInfo := chromedp.Events(eventsCtx, network.ResponseReceivedExtraInfo)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var eventErr error
	var early *Result
	fail := func(err error) {
		mu.Lock()
		if eventErr == nil {
			eventErr = err
		}
		mu.Unlock()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		for e, err := range paused {
			if err != nil {
				if eventsCtx.Err() == nil {
					fail(err)
				}
				return
			}
			if e.ResponseStatusCode != 0 || e.ResponseErrorReason != "" {
				unsafe := false
				if e.FrameID == mainFrame && e.ResourceType == network.ResourceTypeDocument {
					for _, h := range e.ResponseHeaders {
						if privateHeader(h.Name, h.Value) {
							unsafe = true
							break
						}
					}
				}

				if !unsafe && e.FrameID == mainFrame && e.ResourceType == network.ResourceTypeDocument && (e.ResponseStatusCode == 301 || e.ResponseStatusCode == 302 || e.ResponseStatusCode == 303 || e.ResponseStatusCode == 307 || e.ResponseStatusCode == 308) {
					location := ""
					headers := map[string]string{"Content-Type": "text/html; charset=utf-8"}
					for _, h := range e.ResponseHeaders {
						if strings.EqualFold(h.Name, "Location") {
							location = h.Value
						}
						if strings.EqualFold(h.Name, "X-Robots-Tag") || strings.EqualFold(h.Name, "Content-Language") {
							headers[http.CanonicalHeaderKey(h.Name)] = h.Value
						}
					}
					if location == "" {
						fail(errors.New("redirect has no location"))
					} else if mapped, locErr := redirectLocation(p, e.Request.URL, location); locErr != nil {
						fail(locErr)
					} else {
						mu.Lock()
						early = &Result{Status: int(e.ResponseStatusCode), Location: mapped, FinalURL: p.URL, Headers: headers, Renderer: result.Renderer, Redirects: []Redirect{{URL: e.Request.URL, Status: e.ResponseStatusCode, Location: mapped}}}
						mu.Unlock()
					}
					// Deliver a neutral local document to finish navigation without fetching
					// the redirect target. The returned outcome preserves original semantics.
					_, err = chromedp.Call(tab, fetch.FulfillRequest, fetch.FulfillRequestParams{RequestID: e.RequestID, ResponseCode: 200, ResponseHeaders: []*fetch.HeaderEntry{{Name: "Content-Type", Value: "text/html"}}, Body: []byte("<html><head></head><body></body></html>")})
					if err != nil {
						fail(err)
					}
					continue
				}
				if unsafe {
					fail(ErrPrivate)
					_, err = chromedp.Call(tab, fetch.FailRequest, fetch.FailRequestParams{RequestID: e.RequestID, ErrorReason: network.ErrorReasonBlockedByClient})
				} else {
					_, err = chromedp.Call(tab, fetch.ContinueResponse, fetch.ContinueResponseParams{RequestID: e.RequestID})
				}
				if err != nil && eventsCtx.Err() == nil {
					fail(err)
				}
				continue
			}
			allowed := guard.allow(e.Request.URL)
			// Main document navigation stays at the origin, even when CDNs are allowed.
			if e.ResourceType == network.ResourceTypeDocument {
				u, err := url.Parse(e.Request.URL)
				allowed = allowed && err == nil && u.Scheme == origin.Scheme && u.Host == origin.Host
			}
			if !allowed || (e.Request.Method != "GET" && e.Request.Method != "HEAD") {
				guard.blocked.Add(1)
				if e.FrameID == mainFrame && e.ResourceType == network.ResourceTypeDocument {
					fail(errors.New("main navigation blocked"))
				}
				_, err = chromedp.Call(tab, fetch.FailRequest, fetch.FailRequestParams{RequestID: e.RequestID, ErrorReason: network.ErrorReasonBlockedByClient})
			} else {
				headers := make([]*fetch.HeaderEntry, 0, len(e.Request.Headers))
				for k, v := range e.Request.Headers {
					if strings.EqualFold(k, "cookie") || strings.EqualFold(k, "authorization") || strings.EqualFold(k, "proxy-authorization") {
						continue
					}
					headers = append(headers, &fetch.HeaderEntry{Name: k, Value: fmt.Sprint(v)})
				}
				interceptResponse := true
				_, err = chromedp.Call(tab, fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: e.RequestID, Headers: headers, InterceptResponse: &interceptResponse})
			}
			if err != nil && eventsCtx.Err() == nil {
				fail(err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for e, err := range extraInfo {
			if err != nil {
				return
			}
			for k, v := range e.Headers {
				if privateHeader(k, fmt.Sprint(v)) {
					fail(ErrPrivate)
				}
			}
		}
	}()

	defer func() { stopEvents(); wg.Wait() }()
	_, err = chromedp.Call(tab, fetch.Enable, fetch.EnableParams{Patterns: []*fetch.RequestPattern{{URLPattern: "*", RequestStage: fetch.RequestStageRequest}, {URLPattern: "*", RequestStage: fetch.RequestStageResponse}}})
	if err != nil {
		return result, err
	}
	_, err = chromedp.Call(tab, network.SetBypassServiceWorker, network.SetBypassServiceWorkerParams{Bypass: true})
	if err != nil {
		return result, err
	}
	_, err = chromedp.CallBrowser(tab, browser.SetDownloadBehavior, browser.SetDownloadBehaviorParams{Behavior: browser.SetDownloadBehaviorBehaviorDeny})
	if err != nil {
		return result, err
	}
	// Block popup navigation before application code runs. Browser egress remains
	// restricted independently, including for workers and other browser targets.
	_, err = chromedp.Call(tab, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: `window.open=()=>null;`})
	if err != nil {
		return result, err
	}
	response, err := chromedp.Run(tab, chromedp.NavigateResponse(p.Origin))
	if err != nil {
		stopEvents()
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		if eventErr != nil {
			return Result{}, eventErr
		}
		return result, fmt.Errorf("navigate: %w", err)
	}
	if response == nil {
		return result, errors.New("missing document HTTP response")
	}

	mu.Lock()
	earlyCopy := early
	mu.Unlock()
	if earlyCopy != nil {
		stopEvents()
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		if eventErr != nil {
			return Result{}, eventErr
		}
		earlyCopy.Duration = time.Since(started)
		return *earlyCopy, ValidateOutcome(p, *earlyCopy)
	}
	result.Headers = map[string]string{"Content-Type": "text/html; charset=utf-8"}
	for k, v := range response.Headers {
		if strings.EqualFold(k, "X-Robots-Tag") || strings.EqualFold(k, "Content-Language") {
			result.Headers[http.CanonicalHeaderKey(k)] = fmt.Sprint(v)
		}
	}
	result.Status = int(response.Status)

	// Finish response metadata before classifying a redirect or removal. Ordinary
	// HTML keeps the event stream alive until readiness/capture completes.
	if result.Status == 404 || result.Status == 410 {
		stopEvents()
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		if eventErr != nil {
			return Result{}, eventErr
		}
		result.FinalURL = p.URL
		result.Duration = time.Since(started)
		return result, ValidateOutcome(p, result)
	}

	if result.Status == 401 || result.Status == 403 {
		return Result{}, ErrPrivate
	}
	if result.Status != 200 {
		return result, fmt.Errorf("document returned HTTP %d", result.Status)
	}
	if !strings.Contains(strings.ToLower(response.MimeType), "html") {
		return result, errors.New("document is not HTML")
	}
	for k, v := range response.Headers {
		if privateHeader(k, fmt.Sprint(v)) {
			return result, ErrPrivate
		}
	}

	if c.opts.ReadySelector != "" {
		selector, _ := json.Marshal(c.opts.ReadySelector)
		_, err = chromedp.Run(tab, chromedp.Poll[bool]("document.querySelector("+string(selector)+") !== null", chromedp.WithPollingTimeout(0), chromedp.WithPollingInterval(50*time.Millisecond)))
	} else {
		timer := time.NewTimer(c.opts.Settle)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if err != nil {
		return result, fmt.Errorf("readiness: %w", err)
	}
	expression := fmt.Sprintf(`(() => {
 const root=document.documentElement.cloneNode(true);
 root.querySelectorAll('script').forEach(s=>{if((s.getAttribute('type')||'').trim().toLowerCase()!=='application/ld+json')s.remove()});
 [root,...root.querySelectorAll('*')].forEach(e=>{for(const a of Array.from(e.attributes)){if(a.name.toLowerCase().startsWith('on'))e.removeAttribute(a.name)}});
 const html=(document.doctype?'<!DOCTYPE html>\n':'')+root.outerHTML;
 if(new TextEncoder().encode(html).length>%d)throw new Error('snapshot exceeds HTML size limit');
 return {html,final_url:location.href,title:document.title,canonical:document.querySelector('link[rel="canonical"]')?.href||'',robots:document.querySelector('meta[name="robots"]')?.content||''};
 })()`, c.opts.MaxHTMLBytes)
	capture, err := chromedp.Run(tab, chromedp.Evaluate[struct {
		HTML      string `json:"html"`
		FinalURL  string `json:"final_url"`
		Title     string `json:"title"`
		Canonical string `json:"canonical"`
		Robots    string `json:"robots"`
	}](expression))
	if err != nil {
		return result, fmt.Errorf("capture: %w", err)
	}
	if int64(len(capture.HTML)) > c.opts.MaxHTMLBytes {
		return result, errors.New("snapshot exceeds HTML size limit")
	}
	result.FinalURL, err = p.PublicURL(capture.FinalURL)
	if err != nil {
		return result, err
	}
	result.HTML = capture.HTML
	result.Title = capture.Title
	result.Canonical = capture.Canonical
	result.Robots = capture.Robots
	stopEvents()
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if eventErr != nil {
		return Result{}, eventErr
	}
	result.Duration = time.Since(started)
	result.BlockedRequests = guard.blocked.Load()
	return result, nil
}

func privateHeader(name, value string) bool {
	key := strings.ToLower(name)
	value = strings.ToLower(value)
	return key == "set-cookie" || key == "cache-control" && (strings.Contains(value, "private") || strings.Contains(value, "no-store")) || key == "vary" && (strings.Contains(value, "cookie") || strings.Contains(value, "authorization") || strings.TrimSpace(value) == "*")
}
