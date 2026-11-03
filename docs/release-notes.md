RenderCache 0.4.0 saves the content of web components and same-origin frames, so pages built with Lit, Stencil and similar libraries are no longer empty for crawlers that don't run JavaScript.

**Web components.** Their content lives in shadow roots, which aren't part of a page's HTML: before this release a crawler saw `<product-card></product-card>` and nothing inside. RenderCache now writes each open shadow root into the saved copy, with every `<slot>` replaced by the content assigned to it, or by its fallback. Styles scoped to a component are left out so they can't restyle the page. Closed shadow roots can't be read and stay empty. Tested with a real Lit 3.3.3 page in Chrome on Linux, macOS and Windows.

**Same-origin frames.** A frame that loaded a page of the same site with status 200 (or `srcdoc` content, or content a script wrote) becomes a `<div data-rendercache-frame="/path">` with that page's body, up to three frames deep and 20 a page. Its relative links and image addresses are rewritten so they still point where they did, as root-relative paths on your site. Frames from other sites, frames that hadn't loaded yet, error pages and a page framed inside itself stay `<iframe>` elements.

**Settings.** `flatten_shadow_roots` and `flatten_frames`, both on, turn this off to get the earlier capture. `rendercache render` reports `shadow_roots` and `frames` for each page.

**Reviewed.** An independent review went through the change twice before the release. It found eight defects, among them lazy frames that became empty, error pages written into snapshots, and frame links rewritten to the internal origin address. All are fixed, each with a test that failed first.

Archives for Linux (amd64), macOS (Apple silicon) and Windows (amd64), with SHA256SUMS. Known limits are listed in the README and the deployment guide.
