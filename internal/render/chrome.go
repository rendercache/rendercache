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
	// FlattenShadowRoots writes the content of open shadow roots into the
	// captured HTML, with slotted content in its place, so web components
	// (Lit, Stencil and others) aren't empty for crawlers that don't run
	// JavaScript. FlattenFrames does the same for same-origin iframes.
	FlattenShadowRoots bool
	FlattenFrames      bool
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
 const flattenShadow=%t, flattenFrames=%t, maxFrameDepth=3;
 let shadowRoots=0, frames=0, frameBudget=20;
 // Frames of the page itself keep their share of the budget, so frames
 // nested deep inside the first ones can't use it all up.
 let topLeft=Math.min(document.querySelectorAll('iframe').length,frameBudget);
 // Styles of shadow roots and frames are scoped there; written into the page
 // they would restyle it, so they are left out.
 const scopedStyle=n=>n.nodeType===1&&n.getRootNode()!==document&&(n.localName==='style'||(n.localName==='link'&&/(^|\s)stylesheet(\s|$)/i.test(n.getAttribute('rel')||'')));
 // A frame's relative URLs resolve against the frame's own address; written
 // into the page they are rewritten so they still point there. Addresses on
 // this site become root-relative, so the snapshot names the public site,
 // never the origin Chrome renders from; others stay as they are. Only
 // attributes that hold URLs on that element are touched.
 const urlAttrs={href:['a','area','link','use','image'],src:['img','source','iframe','embed','audio','video','track','input','script'],
  data:['object'],action:['form'],formaction:['button','input'],poster:['video'],cite:['blockquote','q','del','ins'],background:['body','table','td','th']};
 const emptyIsSelf={href:1,action:1,formaction:1};
 const rewrite=(v,base,attr)=>{
  const t=v.trim();
  if(t===''&&!emptyIsSelf[attr])return v;
  if(/^(#|javascript:|data:|mailto:|tel:|\/\/)/i.test(t))return v;
  let u;try{u=new URL(t,base)}catch(e){return v}
  return u.origin===location.origin?u.pathname+u.search+u.hash:u.href;
 };
 const srcset=(v,base)=>{ // URL = a run of non-space; descriptors up to the next comma
  const out=[];let i=0;
  while(i<v.length){
   while(i<v.length&&/[\s,]/.test(v[i]))i++;
   if(i>=v.length)break;
   let j=i;while(j<v.length&&!/\s/.test(v[j]))j++;
   let url=v.slice(i,j),tail='';
   if(/,$/.test(url)){url=url.replace(/,+$/,'');i=j}else{let k=j;while(k<v.length&&v[k]!==',')k++;tail=v.slice(j,k).trim();i=k+1}
   out.push(rewrite(url,base,'srcset')+(tail?' '+tail:''));
  }
  return out.join(', ');
 };
 const absolute=(copy,n)=>{
  if(n.ownerDocument===document)return;
  const base=n.baseURI;
  for(const [attr,tags] of Object.entries(urlAttrs)){
   if(!tags.includes(n.localName)||!n.hasAttribute(attr))continue;
   copy.setAttribute(attr,rewrite(n.getAttribute(attr),base,attr));
  }
  if((n.localName==='img'||n.localName==='source')&&n.hasAttribute('srcset'))copy.setAttribute('srcset',srcset(n.getAttribute('srcset'),base));
 };
 const page=u=>u.origin+u.pathname;
 const ancestors=[page(location)];
 // loadedFrame returns the frame's document only when it holds a page of
 // this site that loaded with status 200: not a frame that hasn't loaded
 // (lazy frames hold an empty about:blank), not an error page, not one that
 // went elsewhere, and not a page already being written (a page that frames
 // itself).
 const loadedFrame=(n,depth)=>{
  if(frameBudget<=0||(depth>0&&frameBudget-topLeft<=0))return null;
  let doc=null,win=null;try{doc=n.contentDocument;win=n.contentWindow}catch(e){}
  if(!doc||!doc.body||!win)return null;
  const href=doc.location.href;
  if(n.hasAttribute('srcdoc'))return href==='about:srcdoc'?{doc,key:null}:null;
  const src=(n.getAttribute('src')||'').trim();
  // A frame without a source is about:blank, or the page's own address once
  // a script has opened and written it.
  if(src===''||n.src==='about:blank')return href==='about:blank'||href===location.href?{doc,key:null}:null;
  let want,got;try{want=new URL(n.src);got=new URL(href)}catch(e){return null}
  if(got.origin!==location.origin||page(got)!==page(want)||got.search!==want.search)return null;
  if(ancestors.includes(page(got)))return null;
  const nav=win.performance.getEntriesByType('navigation')[0];
  if(!nav||nav.responseStatus!==200||doc.contentType!=='text/html')return null;
  return {doc,key:page(got)};
 };
 const appendChildren=(from,to,depth)=>{for(const c of from.childNodes){if(scopedStyle(c))continue;const x=compose(c,depth);if(x)to.appendChild(x)}};
 const compose=(n,depth)=>{
  if(n.nodeType!==1)return n.cloneNode(false);
  if(n.localName==='template')return n.cloneNode(true);
  const root=n.getRootNode();
  if(n.localName==='slot'&&root.nodeType===11&&root.host){ // a ShadowRoot of any frame's realm
   const out=document.createDocumentFragment();
   const assigned=n.assignedNodes();
   if(assigned.length){for(const a of assigned){const x=compose(a,depth);if(x)out.appendChild(x)}}else appendChildren(n,out,depth);
   return out;
  }
  const copy=n.cloneNode(false);
  absolute(copy,n);
  if(flattenShadow&&n.shadowRoot){shadowRoots++;appendChildren(n.shadowRoot,copy,depth);return copy}
  if(flattenFrames&&n.localName==='iframe'&&depth<maxFrameDepth){
   const frame=loadedFrame(n,depth);
   if(depth===0&&topLeft>0)topLeft--;
   if(frame){
    frames++;frameBudget--;
    const div=document.createElement('div');
    div.setAttribute('data-rendercache-frame',n.getAttribute('src')||(n.hasAttribute('srcdoc')?'srcdoc':'about:blank'));
    if(n.hasAttribute('title'))div.setAttribute('title',n.getAttribute('title'));
    if(frame.key)ancestors.push(frame.key);
    appendChildren(frame.doc.body,div,depth+1);
    if(frame.key)ancestors.pop();
    return div;
   }
  }
  appendChildren(n,copy,depth);
  return copy;
 };
 const root=(flattenShadow||flattenFrames)?compose(document.documentElement,0):document.documentElement.cloneNode(true);
 root.querySelectorAll('script').forEach(s=>{if((s.getAttribute('type')||'').trim().toLowerCase()!=='application/ld+json')s.remove()});
 [root,...root.querySelectorAll('*')].forEach(e=>{for(const a of Array.from(e.attributes)){if(a.name.toLowerCase().startsWith('on'))e.removeAttribute(a.name)}});
 const html=(document.doctype?'<!DOCTYPE html>\n':'')+root.outerHTML;
 if(new TextEncoder().encode(html).length>%d)throw new Error('snapshot exceeds HTML size limit');
 return {html,final_url:location.href,title:document.title,canonical:document.querySelector('link[rel="canonical"]')?.href||'',robots:document.querySelector('meta[name="robots"]')?.content||'',shadow_roots:shadowRoots,frames};
 })()`, c.opts.FlattenShadowRoots, c.opts.FlattenFrames, c.opts.MaxHTMLBytes)
	capture, err := chromedp.Run(tab, chromedp.Evaluate[struct {
		HTML      string `json:"html"`
		FinalURL  string `json:"final_url"`
		Title     string `json:"title"`
		Canonical string `json:"canonical"`
		Robots    string `json:"robots"`
		Shadow    int    `json:"shadow_roots"`
		Frames    int    `json:"frames"`
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
	result.ShadowRoots = capture.Shadow
	result.Frames = capture.Frames
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
