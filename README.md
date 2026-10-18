# RenderCache

Most AI crawlers don't run JavaScript. If your site is a JavaScript app, GPTBot, ClaudeBot and PerplexityBot see an empty shell. RenderCache sits in front of the app, renders each page once in headless Chrome, keeps the finished HTML on disk and hands it to crawlers. People still get the normal app.

```sh
go build -o bin/rendercache ./cmd/rendercache
./bin/rendercache serve --origin https://app.example.com
curl -I -A GPTBot http://127.0.0.1:8080/    # X-RenderCache: MISS, then HIT once warmed
```

The admin page on `127.0.0.1:9090` shows what a crawler gets next to the raw HTML, which words only appear after rendering, and a log of every crawler delivery. Details follow.

A self-hosted pre-render cache for JavaScript sites, designed to prepare HTML before bots and AI crawlers arrive.

**Alpha prototype — 0.1.0-alpha.2, Linux amd64.** RenderCache warms JavaScript pages from sitemaps, stores rendered responses and serves them to recognized crawler claims. Humans receive the ordinary app. Fresh snapshots return `HIT`; expired snapshots return `STALE` while a deduplicated refresh runs in the background. A cold `MISS` proxies the origin immediately and requests background rendering. The protected admin page compares raw HTML with a saved snapshot, highlights missing text and shows rotating delivery receipts. Snapshot quotas, explicit prune, process locking, constrained HTTP work and Docker/deployment/release packaging are included. This is an evaluation alpha, not a production-readiness claim.

## Run the current checkpoint

Requires Go 1.26 or later. Rendering additionally requires a local Chrome/Chromium executable. The Go binary controls Chrome directly over a pipe; no Node.js service is required.

```sh
go build -o bin/rendercache ./cmd/rendercache
./bin/rendercache serve --origin https://app.example.com
```

The public listener defaults to `:8080`. A separate administration listener defaults to `127.0.0.1:9090`. Terminate with SIGINT or SIGTERM for graceful shutdown. The origin must be a site-root HTTP(S) URL, not the public RenderCache address: using the proxy as its own origin creates a loop.

Try the JavaScript fixture from the repository root in one terminal:

```sh
go run ./examples/spa --public-url https://public.example
```

In another terminal:

```sh
./bin/rendercache serve --config rendercache.example.json
```

Inspect routing:

```sh
curl -i http://127.0.0.1:8080/
curl -i -A GPTBot http://127.0.0.1:8080/
curl -i -A GPTBot -H 'Cookie: session=example' http://127.0.0.1:8080/
curl http://127.0.0.1:9090/status
```

The first and third requests return the original app with `X-RenderCache: BYPASS`. Once warm, the second returns populated HTML with `X-RenderCache: HIT`; before warming it returns `MISS` and the origin shell. Opening the fixture in a browser populates its empty main element from `/api/page`. The fixture sitemap lists `/` and `/about` under its configured public hostname. They warm automatically without a bot visit.

## Scheduled warming

`serve` enables sitemap warming by default. It fetches `/sitemap.xml` from the origin, maps public `loc` URLs to that origin and schedules missing or expired pages through a bounded worker queue. Ordinary proxy requests never wait for discovery or Chrome. To run routing alone, use `--warm=false --snapshots=false`. `--warm=false` alone still permits serving existing disk snapshots without starting Chrome; stale copies then remain available until warming is enabled or a manual render replaces them.

```sh
./bin/rendercache serve --origin https://app.example.com --ready-selector '#content-ready'
./bin/rendercache serve --config rendercache.example.json --sitemap /sitemap-index.xml --warm-interval 6h
curl http://127.0.0.1:9090/status
```

URL sets, nested sitemap indexes, gzip files and HTTP gzip encoding are supported. A single successful discovery defines the current page set; duplicate pages/documents and index cycles are deduplicated. Every page and nested sitemap must belong to `public_url`, and every fetch goes to the configured origin. Redirects must stay on that origin and are limited to five hops. Foreign/invalid locations, malformed XML, unsupported roots/namespaces, XML directives, multiple/missing loc fields or exceeded limits reject the whole scan. A failed scan preserves the last successful page set rather than claiming partial coverage. Nested sitemap documents and page loc fields must be absolute URLs; configured root sitemaps may be root-relative paths. Locations are limited to 8192 bytes. Compressed wire bytes and decompressed XML each have the per-document byte limit.

Writable commands acquire an exclusive data-directory lock and remove recognized abandoned temporary records. Inspect remains read-only and can run alongside serve. Startup counts all recognized regular records for quota accounting, including corrupt files. Quota excess does not delete good copies: reads remain available, writes are rejected unless they shrink/non-grow an existing over-quota set, and explicit `prune --older-than` reclaims old records after stopping the service. See [deployment maintenance](docs/deployment.md). Startup reads and verifies existing snapshots. Recent records are reused without starting Chrome; missing/corrupt/expired ones are scheduled. Missing pages get priority. Only the configured workers can render, each page has at most one queued/active job, and pages beyond queue capacity remain eligible for later dispatch instead of being dropped. Successful saves determine the next refresh time. Render/store failures retain the prior good snapshot and retry with capped exponential backoff. Discovery failures also back off, capped by the normal discovery interval. There is one discovery scan at a time and separate per-fetch and whole-scan deadlines. Scheduling has up to one second of tick granularity when idle; worker completions wake it immediately.

`/status` includes `warming`: ready/warming/discovering/degraded state, discovered/cached/missing/fresh/stale counts, queued and active work, successes/failures, discovery errors/times and up to 100 sorted page records. `pages_omitted` counts the rest; individual errors are capped at 1024 bytes. Cached counts reflect the last successful load/save and eligible bot reads; missing/corrupt reads mark a tracked page missing. This is not a full disk audit on each status request. `ready` means the current discovered set has recent snapshots; an empty valid sitemap has zero discovered pages and can be ready. `/healthz` remains process health, separate from warming success.

Queue entries and retry history are in memory. On restart the sitemap rebuilds them and disk snapshots survive; no persistent job database is claimed. Removed sitemap URLs stop being scheduled and existing files remain until later cleanup. A retiring job may finish writing its old page after rediscovery, but it no longer contributes to current page coverage. Cancellation stops scans and workers, cancels active Chrome renders and waits for cleanup during shutdown.

## Render and retrieve a saved page

With the fixture running, render its finished DOM and save an atomic snapshot:

```sh
./bin/rendercache render --config rendercache.example.json --url / --ready-selector 'main[data-ready]'
./bin/rendercache inspect --config rendercache.example.json --url /
```

Set `--chrome-path /path/to/chrome` if Chrome is not discoverable. `render` returns a compact JSON receipt with its key, hash, size, title and duration. `inspect` returns the complete saved record, including HTML, without launching Chrome. It works in a new process after the origin and browser stop. The sample config maps `https://public.example` to the local fixture origin; production deployments should set `public_url` to their actual public site root. If omitted, it defaults to the origin.

Snapshot keys include the public site, page URL and configured origin. Fragments are discarded and default host ports normalized; path escaping, duplicate query values and query order are preserved. Changing the configured origin creates a different snapshot namespace. HTML and metadata share one JSON record, atomically renamed after file sync, then directory sync. Reads verify the identity, size and HTML hash. A transient render failure never replaces an existing snapshot. Confirmed 404/410 removals replace old HTML with a persisted empty removal response; detected private/session-dependent responses withdraw the old snapshot instead.

Each render uses a fresh temporary Chrome profile, a hard deadline, bounded concurrency and a maximum serialized HTML size. A readiness selector is strongly recommended; without one the worker waits `render_settle` after document load, which cannot prove every asynchronous application has finished. Capture removes executable script elements and inline event-handler attributes from the cloned DOM, preserves JSON-LD and page metadata, and records the renderer version, redirects and final public URL. It is not a general-purpose HTML sanitizer and does not flatten iframe/shadow-root contents. HTTP `X-Robots-Tag` and `Content-Language` are retained for later snapshot serving.

HTML snapshots require HTTP 200 HTML. HTTP redirects and confirmed 404/410 removals are stored as structured responses rather than destination/error HTML. Private/no-store responses, cookie-setting responses and cookie/authorization-dependent Vary responses are rejected, including redirect headers. Raw response metadata is also checked to catch cookie headers Chrome omits from ordinary response objects. Browser requests do not inherit visitor credentials. Conservatively, private/cookie-setting subresource response metadata can also reject a render. HTTP 401/403 are treated as private. Other non-200 statuses remain transient render errors. Private outcomes remove old disk snapshots and suppress serving them until a successful public refresh; failures to remove a file are reported and remain suppressed in the running process.

Browser HTTP(S) traffic is restricted to the origin and explicit `render_allow_origins`, including traffic from worker targets through a local egress guard. Document navigation remains on the configured origin. Cross-origin scripts/fonts/API hosts therefore need explicit permission. Main-tab requests are restricted to GET/HEAD and have cookies/authorization stripped; the guard also strips them from HTTP requests. HTTPS CONNECT traffic is restricted by destination; it is not decrypted by the guard. A fresh browser profile prevents reuse of visitor sessions. For now an app requiring cross-origin iframe navigation or POST-based data loading is outside the supported render path. OS network/container isolation remains part of production deployment, not a claim made by the guard.

The browser loads the configured origin URL directly. Configure the application's canonical/public base URL correctly; origin-host-sensitive applications need special attention because `window.location` reflects that origin. No automatic rewriting of page links or canonical tags is performed. Recognized eligible bot requests receive stored public content. There is no arbitrary public render endpoint.

Chrome's sandbox remains enabled by default, including under root. If an isolated container needs it disabled, explicitly set `chrome_no_sandbox` or pass `--chrome-no-sandbox`; the service never silently turns it off. The initial supported/tested storage platform is Linux, where directory fsync is available.

## Bot serving and refresh API

`snapshot_ttl` controls HTTP freshness independently of `warm_interval`, which controls proactive refresh. A stale hit requests earlier refresh even when the normal warming interval is longer. Queued/active requests coalesce; normal traffic cannot reset failure backoff. An authenticated purge may override backoff. If it arrives during rendering, one follow-up render is scheduled so a deploy hook does not get lost behind work that started before the deploy. Reading an old snapshot cannot cancel a pending purge.

The body is always read and verified from disk. A missing, corrupt or legacy followed-redirect record is a cold miss; an old valid file is retained throughout refresh. No browser work runs synchronously inside the bot handler. `HEAD` returns the same headers/status without a body. HTML `GET`/`HEAD` support ETag/If-None-Match and Last-Modified/If-Modified-Since, with If-None-Match taking precedence. Conditional matching does not convert redirect/removal statuses to 304. Range, write methods, cookies/authorization, non-document Accept values, no-store/no-cache requests and unsupported write preconditions bypass snapshots.

Stored responses use `Cache-Control: private, no-cache` and vary on User-Agent, Cookie, Authorization and Accept so downstream caches must revalidate. They retain selected Content-Language and X-Robots-Tag headers, but never replay Set-Cookie. Cache age is exposed through Age. Transient origin/render failures do not discard good HTML. The stale policy deliberately has no maximum-stale cutoff in this prototype.

Redirects preserve their original status and location without fetching the destination. Origin-host locations are mapped back to the public hostname, with fragments retained; valid external HTTP(S) locations are preserved without allowing browser navigation to them. Persisted removals return 404/410 with an empty body. Redirect/removal records refresh on the same schedule as HTML, allowing recovery when a page returns.

Request-driven pages have a separate `request_page_limit` allowance (default 256). They cannot evict the sitemap set and remain deduplicated across rediscovery. Retired in-flight sitemap jobs are bounded by the queue/workers and can finish. Once the allowance is full, a new URL takes the slot of the least recently requested idle page. Pages that are queued, rendering, waiting for a purge or withdrawn as private keep their slots; only when every slot is busy does a new cold URL proxy without rendering and a purge for it return 429. An evicted page's snapshot stays on disk and is still served; it just stops being refreshed until a crawler asks for it again. Slots reset on restart and do not impose a disk quota. Sitemap coverage counts stay separate from request-driven pages, which have their own count, eviction counter and rejection counter.

Tracking parameters listed in `ignore_query_params` (utm_*, gclid, fbclid and msclkid by default) are dropped from the snapshot identity, so `/story?id=7&utm_source=x` is served the snapshot of `/story?id=7` and does not use a slot of its own. Other parameters keep their order and escaping. The origin still receives the full URL on a miss.

Purge is available only on the administration listener and always requires a configured token, including on loopback. Set the token in the environment before starting `serve`:

```sh
export RENDERCACHE_ADMIN_TOKEN='choose-a-local-secret'
./bin/rendercache serve --config rendercache.example.json
```

From another terminal, supply the same token:

```sh
curl -X POST -H 'Authorization: Bearer choose-a-local-secret' \
  --get --data-urlencode 'url=https://public.example/about' \
  http://127.0.0.1:9090/purge
```

The endpoint accepts exactly one public URL (or root-relative path) and returns 202 with queued/coalesced state while preserving any existing copy. It rejects foreign URLs, API/assets, invalid input and wrong methods. An unconfigured token or disabled warming returns 503; a wrong/missing configured token returns 401. `--snapshots=false` disables serving while allowing scheduled warming to continue.

## Configuration

`--config` loads a strict JSON object; unknown fields and files larger than 1 MiB are rejected. CLI flags `--origin`, `--public-url`, `--listen`, `--admin-listen`, `--data-dir`, `--chrome-path`, `--chrome-no-sandbox`, `--ready-selector`, `--render-timeout`, `--warm`, `--sitemap`, `--warm-interval`, `--sitemap-interval`, `--snapshots` and `--snapshot-ttl` override the corresponding file values. The `RENDERCACHE_ADMIN_TOKEN` and `RENDERCACHE_EDGE_TOKEN` environment variables override their respective JSON secrets, including when explicitly empty. Tokens cannot contain newlines or exceed 1024 bytes. Configuration and bot lists load at startup; restart to apply changes. At most 256 crawler patterns (1..128 bytes each), 64 extra render origins, 128 root sitemaps and a 4096-byte ready selector are accepted. Supplying `bots` replaces the default list, and an empty array disables classification. `--sitemap` replaces `sitemap_urls` with one entry; JSON supports several root sitemaps.

| Setting | Default | Purpose |
| --- | --- | --- |
| `origin` | required | Fixed upstream site root |
| `public_url` | origin | Public root used for URL validation and snapshot identity |
| `data_dir` | `data/snapshots` | Local snapshot and delivery-log directory, one writer process |
| `snapshot_max_bytes` | `1073741824` | Serialized-record quota; 4096 bytes to 1 TiB |
| `snapshot_max_files` | `10000` | Record count quota; 1 to 100000 |
| `max_request_bytes` | `16777216` | Public request-body limit; 1 byte to 256 MiB |
| `http_concurrency` | `32` | Concurrent public requests (1..256); excess requests return 503 |
| `edge_token` | empty | Optional public-listener edge key; prefer environment |
| `delivery_log_max_bytes` | `10485760` | Maximum bytes per log file; 4096 to 1 GiB |
| `delivery_log_backups` | `3` | Rotated files retained in addition to current file; 1 to 10 |
| `chrome_path` | auto-detect | Local browser executable |
| `chrome_no_sandbox` | `false` | Explicit browser sandbox opt-out |
| `render_timeout` | `30s` | Hard deadline including capacity wait/startup |
| `render_settle` | `500ms` | Wait after load when no selector is configured |
| `ready_selector` | empty | CSS selector proving application-specific readiness |
| `max_html_bytes` | 8388608 | Serialized HTML limit, at most 64 MiB |
| `render_concurrency` | 1 | Browser worker slots, range 1–16 |
| `render_allow_origins` | empty | Additional explicit HTTP(S) asset roots |
| `snapshot_enabled` | `true` | Serve saved responses to eligible bots |
| `snapshot_ttl` | `24h` | HTTP freshness age, minimum 1s |
| `request_page_limit` | 256 | Request-driven page allowance with least-recently-requested eviction, range 1–10000 |
| `warm_enabled` | `true` | Enable warming while serving |
| `sitemap_urls` | `["/sitemap.xml"]` | Public root sitemap URLs/paths |
| `warm_interval` | `24h` | Snapshot refresh age; minimum 1s |
| `sitemap_interval` | `15m` | Rediscovery schedule; minimum 1s |
| `warm_retry_base` | `5s` | Initial retry delay; minimum 100ms |
| `warm_retry_max` | `5m` | Maximum retry delay, at least base |
| `warm_queue_size` | 128 | Pending job capacity, range 1–10000 |
| `sitemap_max_bytes` | 8388608 | Wire/XML bytes per document, at most 64 MiB |
| `sitemap_max_urls` | 10000 | Maximum discovered pages, at most 100000 |
| `sitemap_max_documents` | 128 | Maximum sitemap documents, at most 1000 |
| `sitemap_max_depth` | 4 | Maximum index depth, root at 0, at most 16 |
| `sitemap_timeout` | `15s` | Timeout for one HTTP fetch |
| `sitemap_scan_timeout` | `2m` | Deadline for complete discovery |
| `listen` | `:8080` | Public proxy listener |
| `admin_listen` | `127.0.0.1:9090` | Administration listener |
| `admin_token` | empty | Optional bearer token on loopback, required elsewhere |
| `origin_timeout` | `30s` | Upstream response-header timeout |
| `shutdown_timeout` | `10s` | Graceful shutdown deadline |
| `bots` | search, AI and preview crawler patterns, including Google-InspectionTool for Search Console tests | Case-insensitive user-agent substring matches |
| `bypass_paths` | `["/api"]` | Path prefixes that always go to the origin, for example `/graphql` or `/wp-json` |
| `ignore_query_params` | utm_*, gclid, fbclid, msclkid | Query parameters left out of the snapshot identity, case-insensitive |
| `trusted_proxies` | `["127.0.0.0/8", "::1/128"]` | Peers whose X-Forwarded-For/Proto/Host are passed to the origin |

Every admin route requires `Authorization: Bearer <token>` or HTTP Basic authentication (username `rendercache`, password the token) when a token is configured. Browsers receive a Basic challenge. Use HTTPS or a local tunnel for remote administration; Basic and bearer credentials both require transport protection. Use the environment variable rather than committing a real token. `/healthz` reports process health; `/status` reports routing counters and reports warming status and whether automatic warming is enabled; shows whether snapshot serving is enabled. These endpoints are not mounted on the public listener. Public `/status` requests still go to the origin.

Only GET/HEAD document requests without cookies or authorization are eligible for the snapshot path. Paths under `bypass_paths`, non-HTML file extensions, non-HTML Accept headers, range/upgrade requests and `Cache-Control: no-store` bypass it. A user-agent match is a crawler claim, not an authenticated identity. Document responses vary on User-Agent, Cookie, Authorization and Accept; assets and bypassed paths get no added Vary, because crawlers and people receive the same bytes there and the extra Vary would split shared caches. Upstream cookies, status codes and content are preserved. Keep TLS and any trusted edge routing in front of this HTTP service.

Forwarding headers: RenderCache has no TLS listener, so on its own it would tell the origin every request arrived over plain HTTP from the edge's address. When the immediate peer is in `trusted_proxies`, or an `edge_token` is configured and matched, the incoming X-Forwarded-For chain is kept (with the peer appended) and a valid X-Forwarded-Proto (`http` or `https`) and X-Forwarded-Host are passed on. From any other peer these headers are discarded and rebuilt. Without this, apps that force HTTPS from X-Forwarded-Proto redirect crawler misses back to the same URL in a loop. A Forwarded header is never passed on.

Crawler delivery receipts are written as rotating JSON lines under `data_dir/delivery/crawler.jsonl`; see the diagnostics section below. Stderr carries process lifecycle/error messages with bounded waits. The proxy has a total `origin_timeout` deadline (including streaming and upgrades), bounded origin connections/headers, 256/16 public/admin listener connections, public request body limits and a concurrency gate. Oversized known bodies return 413; streamed overflow can abort a request or return a gateway error depending on when detected. Slow uploads are subject to server read deadlines. Use cgroup/container limits for browser RSS, processes and temporary disk. See deployment guide before enabling external routing. Sitemap warming is proactive; eligible misses/stale requests can also request bounded background work. Manual `render` always performs new browser work. Bots' query strings are forwarded unchanged, but query strings are omitted from diagnostic logs.

## What the bot sees

Open `http://127.0.0.1:9090/` on the separate admin listener. When a token is configured, sign in as `rendercache` with the token as password. With no token, the existing loopback-only read/diagnostic policy applies; purge still requires a token. The UI and APIs are never mounted on the public listener. Do not expose an unauthenticated admin listener through a tunnel or proxy.

The page shows sitemap coverage, queue work, errors and up to 100 extra request-page details. Sitemap `ready` describes only discovered sitemap pages; request-page failures have a separate counter. Logs and details refresh with the Refresh status button; comparison and simulation are explicit actions.

Enter a configured-site URL (or `/path`) and select **Compare HTML**. `POST /diagnose` accepts JSON `{"url":"/path"}` and compares a fresh public raw-origin fetch with an existing disk snapshot. It never starts a browser job. A missing snapshot returns 404; use sitemap warming, the manual render command or authenticated purge first. Diagnostics allow two concurrent operations, have a five-second raw-fetch deadline and cap raw and rendered diagnostic HTML at 1 MiB each. Redirects, non-200/non-HTML/private/no-store/cookie-setting responses are withheld. Foreign URLs, API/assets, cross-origin browser submissions and oversized/invalid JSON are rejected. Fetches never inherit admin/visitor credentials, cookies or other headers. Raw bodies are neither persisted nor logged, and diagnostic errors never replace a saved snapshot.

Text extraction ignores head, script, style, template, noscript, hidden and aria-hidden subtrees. A likely shell has at least 10 rendered words, at least 10 words missing from the raw text, and raw text at most 20% of rendered word count. Highlighting compares case-folded word occurrences, stripping surrounding punctuation. This is an approximate diagnostic: it does not compute CSS visibility/layout, handle all languages equally or measure search ranking. Text inserted alongside existing words may be highlighted differently from a phrase diff. Both captures use text nodes, never executable previews; the admin page also enforces a restrictive content security policy.

**Simulate bot fetch** calls `POST /simulate` with the same JSON URL, sends a local credential-free GET using the first configured crawler pattern through the real proxy/snapshot handler, discards its body and emits a receipt marked `simulated`. It can enqueue a cold/stale refresh through normal request handling, but never waits for Chrome. No external crawler visited. A cold origin fetch is limited to five seconds. Empty crawler rules or disabled logging make simulation unavailable.

### Delivery log semantics

Each recognized crawler claim is recorded after response handling, including private/API/protocol bypasses. Events contain a bounded crawler pattern, path without query/fragment, method, actual response status, HIT/STALE/MISS/BYPASS, snapshot age, HTTP body bytes accepted by the response writer and duration. No request headers, raw user agents, query strings, raw page bodies or upstream error strings enter these records. Paths themselves may contain sensitive data; control access and retention accordingly. Differing query variants intentionally share a logged path.

`cached_response` includes saved HTML, redirects and removals. `full_snapshot_html` requires a complete successful GET of a nonempty 200 snapshot with no write failure. HEAD, 304, redirects, removals, misses and partial writes are false. These are server-side write receipts, not proof that a remote client read the body. Protocol upgrades are labeled `protocol_upgrade`; tunneled traffic is outside HTTP body accounting. A user-agent match is a claim, not verified identity; receipts do not prove indexing or AI citation.

Logging uses a 256-event nonblocking queue and one file writer, a 100-event in-memory recent list, 512-byte paths and 128-byte crawler patterns. Defaults retain the current 10 MiB file plus three rotated backups (about 40 MiB maximum). A full queue drops receipts and increments `dropped`; write/open/rotation failures increment `failures` and expose a bounded operation name. These counters and recent receipts appear in `/status` under `delivery_log`. `accepted` means queued; `written` means a successful file write, not fsync durability. Counts/recent receipts reset on restart; files survive. One process must own each data directory; serve/render/prune enforce an advisory lock. Incomplete final JSON lines can occur after a crash or failed write; readers should skip them.

Startup fails if the log cannot be opened. Runtime logging failures do not stall responses or grow files beyond the configured rotation limit. Shutdown closes admission and drains with a two-second deadline; an unresponsive filesystem can leave the writer pending until process exit. Stderr lifecycle writes each have a 100 ms wait limit and report write failures/timeouts as a CLI error; final error output also has a bounded wait. Per-request upstream error text is omitted. Snapshots have independent durability guarantees; these delivery logs are best-effort operational records.

## Prototype checkpoint

Session 2 complete: Chromium renderer, readiness/timeout controls, atomic disk snapshots, checksums and restart persistence. Chrome/Chromium is an external dependency for standalone binaries; the supplied Dockerfile installs it.

Session 3 complete: bounded sitemap/index/gzip discovery, scheduled warming, deduplicated bounded jobs, retry backoff and coverage status.

Session 4 complete: fresh/stale/cold serving, bounded refresh admission, HTTP validators, redirect/removal outcomes, privacy withdrawal and authenticated purge API.

Session 5 complete: raw-versus-rendered shell detection, rotating crawler delivery receipts, bounded request-page error details and one protected status/demo page.

Session 6: quota/cleanup and file confinement, process locking, total HTTP deadlines/admission/body limits, a non-root Chromium image, nginx/Cloudflare examples, and gated deterministic Linux amd64 prerelease artifacts. See [deployment and release verification](docs/deployment.md) for controls, verification boundaries and operational limits.

Lightpanda, BareProxy integration, hosted billing and framework-specific plugins are deferred. Project website: https://rendercache.com.

## Builds and deployment

The supported alpha artifact is Linux amd64; Chrome remains separately installed for the standalone binary. CI tests the extracted executable and compares two deterministic archive builds before a `Release v...` main-branch commit can publish a prerelease with SHA256SUMS. Ordinary commits upload verified artifacts without publishing. Source builds need Go 1.26 or later; the release toolchain is pinned to Go 1.27.2. Other operating systems are currently unsupported because secure file and lock operations are Linux-specific.

The Docker image runs as UID 10001 with Chromium and tini. Its sandbox is enabled by default and requires host support. The isolated CI container smoke explicitly opts out of Chrome's sandbox under a private network and filesystem/resource restrictions; it does not verify sandbox operation on your host. Docker, nginx, Cloudflare routing and quota maintenance instructions are in [docs/deployment.md](docs/deployment.md). No domain, Worker or registry image is deployed automatically.

## Development

```sh
gofmt -w cmd internal examples
go test -race ./...
go vet ./...
go build -o bin/rendercache ./cmd/rendercache
```

Real-browser tests run when `RENDERCACHE_TEST_CHROME` names an executable. They skip when unset, so use this environment for full verification:

```sh
RENDERCACHE_TEST_CHROME=/path/to/chrome go test -race ./...
```

Only if the test environment requires an explicit sandbox opt-out, also set `RENDERCACHE_TEST_NO_SANDBOX=1`. CI locates Chrome and runs these tests in its isolated runner.

Read [RESUME.md](RESUME.md) before continuing development. This checkpoint is foundation code, not a production-ready rendering service.

## License

Apache License 2.0; see [LICENSE](LICENSE).
