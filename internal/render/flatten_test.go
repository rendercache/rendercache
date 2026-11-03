// SPDX-License-Identifier: Apache-2.0
package render

import (
	"context"
	_ "embed"
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
