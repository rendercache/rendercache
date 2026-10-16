# RenderCache — resume instructions

Updated: 2026-10-16. Current checkpoint: session 2 complete and verified. PAUSED after publication; next coding session: 3, only upon user command.

## Authorization and repositories

Work in approximately 2–3 hour scoped sessions. Finish a runnable checkpoint, verify, update this file, commit/push, report and pause. Do not silently begin another session. Do not claim elapsed hours not spent. The user's “Go” authorized session 2. Their later request to check access repaired the GitHub blocker and did not cancel session 2.

Engine: https://github.com/rendercache/rendercache, public, Apache 2.0.
Website: https://github.com/photozeitgeist/rendercache.com, private, existing Hugo site. Access read confirmed. Domain: RenderCache.com; user will connect it later. No website/domain changes made. Inspect website instructions/template before changing it, and do not apply engine-license statements to that private site.

GitHub publication blocker is resolved. The rendercache organization now has an app installation, a real README write succeeded and session 1 was published at 742ba833ea800d5fec8047f9978e4d5d2606b606. The old source archive is a historical fallback, not the current source of truth. Fetch main before continuing and preserve any intervening user changes.

## Accepted product and scope

Self-hosted, warm, logged HTML pre-render cache for public JavaScript apps. Humans receive the ordinary origin app. The completed prototype will pre-warm sitemaps, serve snapshots to bots, keep stale copies during refresh, detect empty shells and show delivery records. One Go binary plus externally installed Chrome/Chromium; later container bundles the browser. Lightpanda, BareProxy integration, hosted billing, framework plugins and PDFs are deferred.

Cold requests will proxy immediately and enqueue work, never wait for a browser. Expired hits will serve the saved copy and deduplicate refreshes. Refresh hooks preserve a usable old copy until replacement succeeds. Serving a response does not prove indexing or AI citation; user-agent matching is a claim, not authentication.

## Completed sessions

Session 1: serve/version/help CLI, strict JSON config and overrides, fixed-origin reverse proxy, editable bot patterns, private-request bypass, BYPASS/MISS headers, separate health/status admin listener and bearer protection, routing diagnostics, graceful shutdown, JavaScript fixture/API/sitemap, Apache license and CI.

Session 2: renderer Engine interface; Chrome implementation using pinned chromedp/CDP modules; fresh profiles and pipe transport; hard timeout, selector-or-settle readiness, semaphore-limited capacity and HTML size cap. Captures status/title/canonical/robots/selected HTTP headers/renderer identity/redirect chain/final public URL, removes executable scripts and inline handlers, preserves JSON-LD. Rejects failed/non-HTML/private/no-store/cookie-setting pages. Request and raw response interception address cookies omitted from ordinary Chrome response metadata.

A loopback egress guard restricts browser HTTP(S) destinations to origin/explicit asset roots and covers workers independently of main-tab interception. Main document navigation stays on origin. Main-tab cookie/auth headers are removed; HTTP guard also strips credentials. HTTPS tunnels are destination-restricted, not decrypted. No visitor request headers are passed to Chrome. Chrome sandbox is enabled by default; explicit opt-out exists for isolated environments.

Canonical target mapping namespaces keys by public site, URL and origin, preserves path escaping/query order, drops fragments, normalizes host/default ports, rejects foreign targets. Disk store saves one versioned HTML-plus-metadata record through temp file/file fsync/rename/directory fsync; verifies key, length and HTML SHA-256 on read. Concurrent readers never see mixed HTML/metadata. Invalid or failed renders preserve prior snapshots. Added render and inspect commands; inspect works without Chrome or a live origin.

## Not implemented yet

No scheduled sitemap warming, persisted queue, automatic snapshot serving, TTL lifecycle, purge endpoint, rotating bot log or status/demo HTML UI. During serve all traffic still goes upstream and bots receive MISS. render is an explicit manual operation that always renders; inspect is an offline disk read. Do not announce a complete cache service or publish a release yet.

## Verification

Local Linux amd64: Go 1.27.2, Chrome Headless Shell 155.0.8059.84. Race-enabled tests pass across all seven packages with real Chrome enabled. Checks include dynamic DOM capture, JSON-LD/metadata/script handling, denied off-origin main and worker requests, same-origin and blocked redirects, cookie/no-store/private rejection, HTTP status and MIME rejection, readiness deadline, missing executable, output size, capacity-wait cancellation, CLI render/inspect, target mapping, restart persistence, checksum corruption, failed write preservation, failed rename cleanup, concurrent atomic replaces, existing proxy routing and shutdown. go vet and build pass. Live subprocess render/inspect smoke passed: captured 627 bytes of populated fixture HTML, preserved JSON-LD, removed executable script, retained old snapshot on a failed render and restored identical data in a new process after origin and renderer termination. Module checksum verification passed. Session-one GitHub CI succeeded at 742ba833ea800d5fec8047f9978e4d5d2606b606. Session-two CI runs on publication; inspect the latest Actions run for its result.

Chrome privacy testing initially found Set-Cookie absent from ordinary and Fetch response headers. Raw Network.responseReceivedExtraInfo metadata now rejects those responses; a regression covers direct and redirect cookie-setting pages. This is intentionally conservative: private/cookie-setting subresource metadata can also reject a render.

## Known limits and design decisions

Storage durability verified on Linux; directory fsync on other operating systems is not verified. Each render starts a fresh browser for isolation; pooling is deferred. Asset origins are explicit and POST-driven data loading/cross-origin document frames are outside the current path. Default settling does not guarantee readiness: configure a real selector. Captures do not flatten shadow roots/iframes and are not a security sanitizer. Keep future demo previews sandboxed.

The browser loads origin URLs directly. An app using location.host sees the origin host; configure its public/canonical base URL correctly. Stored HTML links are not rewritten. Cache failures currently reject all non-200 statuses; session 4 must add explicit removal/tombstone behavior and preserve redirect semantics instead of blindly serving final HTML at an original redirect URL. The guard is an application control, not a replacement for OS egress/container isolation.

## Exact next work: session 3

Read AGENTS.md and this file, fetch current main and run the baseline. Build sitemap URL-set, sitemap-index and gzip parsing with document size, nesting and discovered-URL limits. Public sitemap loc URLs must pass target.Map and be fetched/rendered against the configured origin without looping through the proxy. Constrain redirects and external sitemap locations. Add a bounded, deduplicated warming queue using the existing render.Engine and snapshot.Store; one shared Chrome instance can enforce configured concurrency. Warm missing pages at startup and refresh on schedule with retry backoff. Define cancellation and shutdown behavior so no worker/browser leaks. Add coverage/queue/failure status JSON. Use fake engines for queue scheduling tests and the real JavaScript fixture for end-to-end warming. Preserve public/private eligibility and the existing server's immediate proxy behavior; automatic snapshot serving is session 4. Document fields, update this file, commit/push, report and pause.

Session 4: fresh/stale/cold serving, TTL, deduplicated background refresh, failed-refresh preservation, removal and redirect semantics, authenticated refresh/purge API.
Session 5: shell detector, rotating delivery logs, protected status page and side-by-side demo with highlighted missing text and labeled simulated bot request.
Session 6: end-to-end hardening, storage/resource limits, Docker, nginx/Cloudflare instructions and tested release artifacts.

## Reproduction

From repo root: go test -race ./...; go vet ./...; go build -o bin/rendercache ./cmd/rendercache. For real-browser tests set RENDERCACHE_TEST_CHROME to the browser path; set RENDERCACHE_TEST_NO_SANDBOX=1 only for an isolated environment requiring it. CI is configured to run real browser checks.

Run go run ./examples/spa, then bin/rendercache render --config rendercache.example.json --url / --chrome-path /path/to/chrome. Inspect with bin/rendercache inspect --config rendercache.example.json --url /. Add --chrome-no-sandbox only if needed in an isolated container. Normal serve behavior remains as in session 1.

Temporary local tools for this session: /tmp/rendercache-toolchain/go/bin/go and /tmp/rendercache-chrome/chrome-headless-shell-linux64/chrome-headless-shell. Do not assume they survive environment changes.
