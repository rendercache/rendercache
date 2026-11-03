// SPDX-License-Identifier: Apache-2.0
package render

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rendercache/rendercache/internal/detect"
	"github.com/rendercache/rendercache/internal/target"
)

//go:embed testdata/lit/lit.js
var litBundle string

const litPage = `<!doctype html><html><head><title>Pottery shop</title>
<script type="application/ld+json">{"@type":"Product","name":"Blue Mug"}</script>
<script type="module">
import {LitElement, html, css} from '/lit.js';
class ProductCard extends LitElement {
  static properties = {name: {}};
  static styles = css` + "`h2{color:rgb(1,2,3)}`" + `;
  render() {
    return html` + "`" + `<article><h2>${this.name}</h2><p>Hand-thrown stoneware, glazed in small batches.</p><slot></slot><slot name="price">Price on request</slot></article>` + "`" + `;
  }
}
customElements.define('product-card', ProductCard);
class ShopPage extends LitElement {
  render() {
    return html` + "`" + `<main><product-card name="Blue Mug"><em>Ships in two days</em></product-card><product-card name="Green Bowl"><b slot="price">24 euros</b></product-card></main>` + "`" + `;
  }
}
customElements.define('shop-page', ShopPage);
class Sealed extends HTMLElement { constructor() { super(); this.attachShadow({mode: 'closed'}).innerHTML = '<p>Closed shadow text</p>'; } }
customElements.define('sealed-box', Sealed);
</script></head><body><shop-page></shop-page><sealed-box></sealed-box>
<iframe src="/terms" title="Delivery terms"></iframe>
<iframe srcdoc="<p>Inline frame text</p><style>body{color:rgb(4,5,6)}</style>"></iframe>
</body></html>`

func litOrigin(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lit.js":
			w.Header().Set("Content-Type", "text/javascript")
			io.WriteString(w, litBundle)
		case "/terms":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<!doctype html><title>Terms</title><style>p{color:rgb(7,8,9)}</style><p>Orders ship within two working days.</p><iframe src="/terms-inner"></iframe><script>document.write('<p>Written by a frame script</p>')</script>`)
		case "/terms-inner":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<p>Returns accepted for thirty days.</p>`)
		default:
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, litPage)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func renderLit(t *testing.T, shadow, frames bool) Result {
	t.Helper()
	opts := browserOptions(t)
	opts.ReadySelector = ""
	opts.Settle = 1500 * time.Millisecond
	opts.FlattenShadowRoots, opts.FlattenFrames = shadow, frames
	engine, err := NewChrome(opts)
	if err != nil {
		t.Fatal(err)
	}
	srv := litOrigin(t)
	p, err := target.Map(srv.URL, srv.URL, "/")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := engine.Render(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// The 0.4 promise: a Lit page's text is in the saved HTML, where a crawler
// that doesn't run JavaScript can read it.
func TestLitComponentTextIsCaptured(t *testing.T) {
	res := renderLit(t, true, true)
	text := detect.Text(res.HTML)
	for _, want := range []string{"Blue Mug", "Hand-thrown stoneware, glazed in small batches.", "Ships in two days", "Price on request",
		"Green Bowl", "24 euros", "Orders ship within two working days.", "Returns accepted for thirty days.", "Written by a frame script", "Inline frame text"} {
		if !strings.Contains(text, want) {
			t.Errorf("crawler text lacks %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "Price on request") != 1 {
		t.Error("slot fallback shown although content was assigned to the slot")
	}
	for _, unwanted := range []string{"<slot", "rgb(1, 2, 3)", "rgb(1,2,3)", "rgb(4,5,6)", "rgb(7,8,9)", "<script type=\"module\"", "document.write", "Closed shadow text"} {
		if strings.Contains(res.HTML, unwanted) {
			t.Errorf("captured HTML contains %q", unwanted)
		}
	}
	if !strings.Contains(res.HTML, `application/ld+json`) {
		t.Error("structured data dropped")
	}
	if !strings.Contains(res.HTML, `data-rendercache-frame="/terms"`) || !strings.Contains(res.HTML, `title="Delivery terms"`) {
		t.Error("frame not marked")
	}
	if res.ShadowRoots != 3 || res.Frames != 3 {
		t.Errorf("shadow roots %d (want 3), frames %d (want 3)", res.ShadowRoots, res.Frames)
	}
}

func TestFlatteningCanBeTurnedOff(t *testing.T) {
	res := renderLit(t, false, false)
	text := detect.Text(res.HTML)
	if strings.Contains(text, "Hand-thrown") || strings.Contains(text, "Orders ship") {
		t.Errorf("flattening off, yet component or frame text captured: %s", text)
	}
	if res.ShadowRoots != 0 || res.Frames != 0 || !strings.Contains(res.HTML, "<iframe") {
		t.Errorf("flattening off changed the capture: %d %d", res.ShadowRoots, res.Frames)
	}
}

// Review of 0.4 Phase B: each case failed on 0ac28e6.
const reviewPage = `<!doctype html><html><head><title>Frames</title></head><body>
<div style="height:6000px"></div>
<iframe src="/samelazy" loading="lazy" title="lazy"></iframe>
<iframe src="/widgets/box.html"></iframe>
<iframe src="/broken"></iframe>
<iframe src="/fsinner"></iframe>
<iframe src="/selfloop"></iframe>
<p>page end</p></body></html>`

func reviewOrigin(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/samelazy":
			io.WriteString(w, `<p>Lazy frame body</p>`)
		case "/widgets/box.html":
			io.WriteString(w, `<a href="item.html">relative link</a><img src="pic.png" srcset="pic.png 1x, big/pic.png 2x">`)
		case "/broken":
			w.WriteHeader(500)
			io.WriteString(w, `<p>Internal Server Error stack trace</p>`)
		case "/fsinner":
			io.WriteString(w, `<x-box><span>LIGHT DOM PROJECTED</span></x-box><script>customElements.define('x-box',class extends HTMLElement{constructor(){super();this.attachShadow({mode:'open'}).innerHTML='<div><slot>FALLBACK SHOWN</slot></div>'}})</script>`)
		case "/selfloop":
			io.WriteString(w, `<p>loop</p><iframe src="/selfloop"></iframe><iframe src="/selfloop?x"></iframe>`)
		default:
			io.WriteString(w, reviewPage)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReview040Frames(t *testing.T) {
	opts := browserOptions(t)
	opts.ReadySelector = ""
	opts.Settle = 1500 * time.Millisecond
	opts.FlattenShadowRoots, opts.FlattenFrames = true, true
	engine, err := NewChrome(opts)
	if err != nil {
		t.Fatal(err)
	}
	srv := reviewOrigin(t)
	p, _ := target.Map(srv.URL, srv.URL, "/")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := engine.Render(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	h := res.HTML
	// 1. A frame that never loaded keeps its iframe.
	if !strings.Contains(h, `<iframe src="/samelazy" loading="lazy" title="lazy"></iframe>`) {
		t.Errorf("unloaded lazy frame not kept as an iframe")
	}
	// 2. Slots inside a frame are resolved.
	if !strings.Contains(h, "LIGHT DOM PROJECTED") || strings.Contains(h, "FALLBACK SHOWN") || strings.Contains(h, "<slot") {
		t.Errorf("slot inside a frame not resolved")
	}
	// 3. Links and images in a frame keep pointing where they did (root-relative).
	for _, want := range []string{`href="/widgets/item.html"`, `src="/widgets/pic.png"`, `/widgets/big/pic.png 2x`} {
		if !strings.Contains(h, want) {
			t.Errorf("frame URL not rewritten: want %s", want)
		}
	}
	// 4. An error page is not written into the snapshot.
	if strings.Contains(h, "stack trace") || !strings.Contains(h, `<iframe src="/broken">`) {
		t.Errorf("frame error page written into the snapshot")
	}
	// A page framing itself is written once, not k + k² + k³ times.
	if n := strings.Count(h, "<p>loop</p>"); n != 1 {
		t.Errorf("self-framing page written %d times", n)
	}
}

// Second review round of 0.4 Phase B: each case failed on 313a493.
func TestReview040bFrameURLsUsePublicSite(t *testing.T) {
	opts := browserOptions(t)
	opts.ReadySelector = ""
	opts.Settle = 1500 * time.Millisecond
	opts.FlattenShadowRoots, opts.FlattenFrames = true, true
	engine, err := NewChrome(opts)
	if err != nil {
		t.Fatal(err)
	}
	var top strings.Builder
	top.WriteString(`<!doctype html><title>t</title><iframe src="/sub/attrs.html"></iframe><iframe srcdoc="<a href='sdrel'>srcdoc link</a>"></iframe>`)
	for i := 1; i <= 3; i++ {
		fmt.Fprintf(&top, `<iframe src="/n%d"></iframe>`, i)
	}
	top.WriteString(`<iframe src="/last"></iframe><iframe id="filled"></iframe><script>const d=document.getElementById('filled').contentDocument;d.open();d.write('<p>Filled by script</p>');d.close()</script>`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch {
		case r.URL.Path == "/sub/attrs.html":
			io.WriteString(w, `<a href="attrs.html?page=2">next</a><a href="">self</a><a href="//cdn.example/x">cdn</a>`+
				`<img src="pic.png" srcset="data:image/gif;base64,R0lGODlhAQABAAAAACw= 1x, https://img.example/upload/w_100,h_50/a.jpg 2x, big/pic.png 3x">`+
				`<div data="notaurl" href="nothing" src="custom">plain</div><svg><use href="sprite.svg#i"></use></svg>`)
		case r.URL.Path == "/last":
			io.WriteString(w, `<p>LAST TOP FRAME</p>`)
		case strings.HasPrefix(r.URL.Path, "/n"):
			fmt.Fprintf(w, `<p>%s</p>`, r.URL.Path)
			if len(r.URL.Path) < 6 {
				for i := 1; i <= 3; i++ {
					fmt.Fprintf(w, `<iframe src="%s%d"></iframe>`, r.URL.Path, i)
				}
			}
		default:
			io.WriteString(w, top.String())
		}
	}))
	t.Cleanup(srv.Close)
	p, err := target.Map("https://public.example", srv.URL, "/")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := engine.Render(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	h := res.HTML
	if strings.Contains(h, strings.TrimPrefix(srv.URL, "http://")) {
		t.Errorf("snapshot names the origin address %s", srv.URL)
	}
	for _, want := range []string{`href="/sub/attrs.html?page=2"`, `href="/sub/attrs.html"`, `href="//cdn.example/x"`, `src="/sub/pic.png"`,
		`data:image/gif;base64,R0lGODlhAQABAAAAACw= 1x`, `https://img.example/upload/w_100,h_50/a.jpg 2x`, `/sub/big/pic.png 3x`,
		`<div data="notaurl" href="nothing" src="custom">`, `<use href="/sub/sprite.svg#i">`, `href="/sdrel"`, "LAST TOP FRAME", "Filled by script"} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %s", want)
		}
	}
}
