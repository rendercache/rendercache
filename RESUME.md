# RenderCache — resume instructions

Updated: 2026-10-16. Current checkpoint: session 3 complete and verified locally. PAUSED after publication; next coding session: 4, only upon user command.

## Authorization and repositories

Work in approximately 2–3 hour scoped sessions. Finish a runnable checkpoint, verify, update this file, commit/push, report and pause. Do not silently begin another session. Do not claim elapsed hours not spent. The user's latest “Go” authorized session 3. The earlier request to check access repaired the GitHub blocker during session 2.

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

Session 3: sitemap URL sets/indexes/gzip discovery with root namespace/XML validation, URL/document/depth/byte limits, an 8192-byte loc limit, same-site loc restrictions, origin-only fetching, constrained redirects, HTTP and whole-scan deadlines. Whole discovery replaces the set only on success; failures keep the prior set. Added a bounded worker queue with per-page queued/active flags, missing-page priority, deferred admission under queue pressure, scheduled refresh, capped retry backoff, startup disk reuse and cancellation/cleanup. Shared renderer enforces configured concurrency. No bot request triggers or waits for this work. Serve enables warming by default; --warm=false disables it. Admin status reports coverage, queue, active work, discovery state/errors and at most 100 page details. Example fixture can emit sitemap loc URLs under --public-url.

## Not implemented yet

No automatic snapshot serving, request-driven TTL lifecycle, purge endpoint, rotating bot log or status/demo HTML UI. Queue and retry state are deliberately in memory; startup reconstructs them from sitemap and durable snapshots. During serve warming now runs independently, but visitor traffic still goes upstream and bots receive MISS. render is an explicit manual operation that always renders; inspect is an offline disk read. Do not announce a complete cache service or publish a release yet.

## Verification

Local Linux amd64: Go 1.27.2, Chrome Headless Shell 155.0.8059.84. Race-enabled tests pass across all nine packages with real Chrome enabled. Checks include dynamic DOM capture, JSON-LD/metadata/script handling, denied off-origin main and worker requests, same-origin and blocked redirects, cookie/no-store/private rejection, HTTP status and MIME rejection, readiness deadline, missing executable, output size, capacity-wait cancellation, CLI render/inspect, target mapping, restart persistence, checksum corruption, failed write preservation, failed rename cleanup, concurrent atomic replaces, existing proxy routing and shutdown. go vet and build pass. Live subprocess render/inspect smoke passed: captured 627 bytes of populated fixture HTML, preserved JSON-LD, removed executable script, retained old snapshot on a failed render and restored identical data in a new process after origin and renderer termination. Module checksum verification passed. Session-one GitHub CI succeeded at 742ba833ea800d5fec8047f9978e4d5d2606b606. Session-two GitHub CI also passed at 443551984ed2898c1accf03a201f92cf8df36a49. Inspect the latest Actions run for session-three CI status after publication.

Session-three checks cover gzip/index parsing, repeated indexes/cycles, malformed XML, foreign pages/indexes/redirects, byte/page/document/depth limits and cancellation; queue bounds/concurrency, deduplication, eventual admission beyond capacity, fresh restart reuse, scheduled refresh, retry delay, failed-render stale preservation, corrupt snapshot repair, failed discovery retention/recovery and worker shutdown. The real-Chrome serve integration warmed two public URLs before any proxy request and reused both on restart with an invalid Chrome path. Live executable smoke also warmed 2 pages before any bot, reached 4 successful renders after scheduled refresh, retained session-3 bot MISS behavior, reused 2 snapshots with zero new renders after restart and exited cleanly on SIGTERM. Race tests, go vet and builds passed.

Chrome privacy testing initially found Set-Cookie absent from ordinary and Fetch response headers. Raw Network.responseReceivedExtraInfo metadata now rejects those responses; a regression covers direct and redirect cookie-setting pages. This is intentionally conservative: private/cookie-setting subresource metadata can also reject a render.

## Known limits and design decisions

Storage durability verified on Linux; directory fsync on other operating systems is not verified. Each render starts a fresh browser for isolation; pooling is deferred. Asset origins are explicit and POST-driven data loading/cross-origin document frames are outside the current path. Default settling does not guarantee readiness: configure a real selector. Captures do not flatten shadow roots/iframes and are not a security sanitizer. Keep future demo previews sandboxed.

The browser loads origin URLs directly. An app using location.host sees the origin host; configure its public/canonical base URL correctly. Stored HTML links are not rewritten. Cache failures currently reject all non-200 statuses; session 4 must add explicit removal/tombstone behavior and preserve redirect semantics instead of blindly serving final HTML at an original redirect URL. The guard is an application control, not a replacement for OS egress/container isolation.

Session-three operational limits: the scheduler tick is up to 1s when idle. Discovery errors retry with capped backoff no slower than the configured discovery interval. A removed sitemap entry is unscheduled but its file is not deleted; an already running retired job can finish writing its old file. Cache counts reflect successful load/save, not an audit on every status read. Whole discovery can be ready with an empty page set; always inspect discovered counts. Config requires refresh/discovery intervals at least 1s and retry base at least 100ms to avoid tight loops. Byte limits are per document, with a whole-scan time limit rather than a total cross-document byte budget. Default limits bound the page set; further resource hardening is session 6.

## Exact next work: session 4

Read AGENTS.md and this file, fetch current main and run the baseline. Add bot snapshot serving to the existing proxy without making bot responses wait for Chrome. Preserve human/private/API/asset/HEAD/range/upgrade routing behavior. Fresh snapshots return immediately; stale snapshots remain available while background refresh runs; cold misses immediately proxy origin and enqueue work. Add a bounded, deduplicated request/refresh admission API to the existing warmer (it currently discovers only sitemap pages). Define how out-of-sitemap requests interact with the page limit and current coverage; do not let arbitrary bot URLs create unbounded state or evict the sitemap set.

Separate HTTP snapshot freshness/TTL policy from warming schedule explicitly. Handle store load/corruption/missing-file errors as cold misses and update coverage accordingly (current counts are last loaded/saved metadata). Preserve last good files on transient render failures. Implement explicit removal/tombstone behavior for confirmed 404/410, and preserve redirect status/location instead of serving a redirected document as 200 at the original URL. Existing render.Engine reports non-200 as an error and Result.Redirects records origin URLs: extend its structured outcome deliberately rather than parsing error strings. Preserve HTTP X-Robots-Tag/content language and page metadata, do not replay cookies, and keep conditional/HEAD response semantics correct.

Implement authenticated POST /purge?url=... on the separate admin listener, scheduling refresh without deleting a usable old snapshot. Honor current token/loopback rules. Add meaningful integration checks for fresh/stale/cold paths, duplicate request pressure, failed refresh preservation, expired/deleted files, removals, redirects, private requests, metadata, token checks and shutdown. Use fake engines for timing and the real fixture for end-to-end serving. Update documentation and this file, commit/push, check CI, report and pause. Do not start session 5 automatically.

Session 5: shell detector, rotating delivery logs, protected status page and side-by-side demo with highlighted missing text and labeled simulated bot request.
Session 6: end-to-end hardening, storage/resource limits, Docker, nginx/Cloudflare instructions and tested release artifacts.

## Reproduction

From repo root: go test -race ./...; go vet ./...; go build -o bin/rendercache ./cmd/rendercache. For real-browser tests set RENDERCACHE_TEST_CHROME to the browser path; set RENDERCACHE_TEST_NO_SANDBOX=1 only for an isolated environment requiring it. CI is configured to run real browser checks.

Run go run ./examples/spa, then bin/rendercache render --config rendercache.example.json --url / --chrome-path /path/to/chrome. Inspect with bin/rendercache inspect --config rendercache.example.json --url /. Add --chrome-no-sandbox only if needed in an isolated container. Serve now warms sitemap pages by default; ordinary proxy responses remain unchanged until session 4. Run the fixture with --public-url https://public.example to match the sample configuration. Set --warm=false for routing-only use.

Temporary local tools for this session: /tmp/rendercache-toolchain/go/bin/go and /tmp/rendercache-chrome/chrome-headless-shell-linux64/chrome-headless-shell. Do not assume they survive environment changes.
