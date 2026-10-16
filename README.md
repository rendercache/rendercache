# RenderCache

A self-hosted pre-render cache for JavaScript sites, designed to prepare HTML before bots and AI crawlers arrive.

**Prototype in progress — session 4 of 6.** The service warms JavaScript pages from sitemaps, stores rendered responses and serves them to recognized bots. Humans receive the ordinary app. Fresh snapshots return `HIT`; expired snapshots return `STALE` while a deduplicated refresh runs in the background. A cold `MISS` proxies the origin immediately and requests background rendering. Shell detection, the richer status/demo UI and release packaging are still upcoming.

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

Startup reads and verifies existing snapshots. Recent records are reused without starting Chrome; missing/corrupt/expired ones are scheduled. Missing pages get priority. Only the configured workers can render, each page has at most one queued/active job, and pages beyond queue capacity remain eligible for later dispatch instead of being dropped. Successful saves determine the next refresh time. Render/store failures retain the prior good snapshot and retry with capped exponential backoff. Discovery failures also back off, capped by the normal discovery interval. There is one discovery scan at a time and separate per-fetch and whole-scan deadlines. Scheduling has up to one second of tick granularity when idle; worker completions wake it immediately.

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

Request-driven pages have a separate `request_page_limit` allowance (default 256). They cannot evict the sitemap set and remain deduplicated across rediscovery. Retired in-flight sitemap jobs are bounded by the queue/workers and can finish. Once the request allowance is full, additional cold URLs continue to proxy and stale URLs can still serve their saved copies; a purge for a new URL returns 429. These slots are not evicted during the process lifetime and reset on restart. They do not impose a disk quota. Sitemap coverage counts stay separate from request-driven pages, which have their own count and rejection counter.

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

`--config` loads a strict JSON object; unknown fields and files larger than 1 MiB are rejected. CLI flags `--origin`, `--public-url`, `--listen`, `--admin-listen`, `--data-dir`, `--chrome-path`, `--chrome-no-sandbox`, `--ready-selector`, `--render-timeout`, `--warm`, `--sitemap`, `--warm-interval`, `--sitemap-interval`, `--snapshots` and `--snapshot-ttl` override the corresponding file values. The `RENDERCACHE_ADMIN_TOKEN` environment variable overrides `admin_token`, including when explicitly empty. Configuration and bot lists load at startup; restart to apply changes. Supplying `bots` replaces the default list, and an empty array disables classification. `--sitemap` replaces `sitemap_urls` with one entry; JSON supports several root sitemaps.

| Setting | Default | Purpose |
| --- | --- | --- |
| `origin` | required | Fixed upstream site root |
| `public_url` | origin | Public root used for URL validation and snapshot identity |
| `data_dir` | `data/snapshots` | Local record directory |
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
| `request_page_limit` | 256 | Separate request-driven page allowance, range 1–10000 |
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
| `bots` | search, AI and preview crawler patterns | Case-insensitive user-agent substring matches |

Every admin route requires `Authorization: Bearer <token>` when a token is configured. Use the environment variable rather than committing a real token. `/healthz` reports process health; `/status` reports routing counters and reports warming status and whether automatic warming is enabled; shows whether snapshot serving is enabled. These endpoints are not mounted on the public listener. Public `/status` requests still go to the origin.

Only GET/HEAD document requests without cookies or authorization are eligible for the snapshot path. API paths, non-HTML file extensions, non-HTML Accept headers, range/upgrade requests and `Cache-Control: no-store` bypass it. A user-agent match is a crawler claim, not an authenticated identity. Every upstream response varies on User-Agent, Cookie and Authorization; upstream cookies, status codes and content are preserved. Incoming forwarding claims are removed before fresh forwarding headers are generated. Keep TLS and any trusted edge routing in front of this HTTP service.

Current JSON stderr diagnostics distinguish HIT/STALE/MISS and whether HTML bytes were delivered. They are not yet the persistent rotating delivery log planned for session five. Sitemap warming is proactive; eligible misses/stale requests can also request bounded background work. Manual `render` always performs new browser work. Bots' query strings are forwarded unchanged, but query strings are omitted from diagnostic logs.

## Roadmap

Session 2 complete: Chromium renderer, readiness/timeout controls, atomic disk snapshots, checksums and restart persistence. Chrome/Chromium is an external dependency; the later container will bundle it.

Session 3 complete: bounded sitemap/index/gzip discovery, scheduled warming, deduplicated bounded jobs, retry backoff and coverage status.

Session 4 complete: fresh/stale/cold serving, bounded refresh admission, HTTP validators, redirect/removal outcomes, privacy withdrawal and authenticated purge API.

Session 5: raw-versus-rendered shell detection, rotating bot delivery logs and one protected status/demo page.

Session 6: complete integration checks, Docker packaging, deployment guides and initial release artifacts.

Lightpanda, BareProxy integration, hosted billing and framework-specific plugins are deferred. Project website: https://RenderCache.com (domain connection pending).

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
