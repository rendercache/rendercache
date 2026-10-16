# RenderCache

A self-hosted pre-render cache for JavaScript sites, designed to prepare HTML before bots and AI crawlers arrive.

**Prototype in progress — session 2 of 6.** The reverse proxy and bot classification work. Manual Chrome rendering and persistent disk snapshots now work through the `render` and `inspect` commands. Scheduled warming and automatic snapshot serving are still upcoming. During `serve`, all requests currently reach the origin; recognized public bot document requests are marked `MISS`.

## Run the current checkpoint

Requires Go 1.26 or later. Rendering additionally requires a local Chrome/Chromium executable. The Go binary controls Chrome directly over a pipe; no Node.js service is required.

```sh
go build -o bin/rendercache ./cmd/rendercache
./bin/rendercache serve --origin https://app.example.com
```

The public listener defaults to `:8080`. A separate administration listener defaults to `127.0.0.1:9090`. Terminate with SIGINT or SIGTERM for graceful shutdown. The origin must be a site-root HTTP(S) URL, not the public RenderCache address: using the proxy as its own origin creates a loop.

Try the JavaScript fixture from the repository root in one terminal:

```sh
go run ./examples/spa
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

The first and third requests return `X-RenderCache: BYPASS`; the second returns `X-RenderCache: MISS`. All three contain the same original application shell at this stage. Opening the fixture in a JavaScript-capable browser populates its empty main element from `/api/page`. The `/about` page and `/sitemap.xml` are included for rendering and subsequent warming tests.

## Render and retrieve a saved page

With the fixture running, render its finished DOM and save an atomic snapshot:

```sh
./bin/rendercache render --config rendercache.example.json --url / --ready-selector 'main[data-ready]'
./bin/rendercache inspect --config rendercache.example.json --url /
```

Set `--chrome-path /path/to/chrome` if Chrome is not discoverable. `render` returns a compact JSON receipt with its key, hash, size, title and duration. `inspect` returns the complete saved record, including HTML, without launching Chrome. It works in a new process after the origin and browser stop. The sample config maps `https://public.example` to the local fixture origin; production deployments should set `public_url` to their actual public site root. If omitted, it defaults to the origin.

Snapshot keys include the public site, page URL and configured origin. Fragments are discarded and default host ports normalized; path escaping, duplicate query values and query order are preserved. Changing the configured origin creates a different snapshot namespace. HTML and metadata share one JSON record, atomically renamed after file sync, then directory sync. Reads verify the identity, size and HTML hash. A failed render never replaces an existing snapshot.

Each render uses a fresh temporary Chrome profile, a hard deadline, bounded concurrency and a maximum serialized HTML size. A readiness selector is strongly recommended; without one the worker waits `render_settle` after document load, which cannot prove every asynchronous application has finished. Capture removes executable script elements and inline event-handler attributes from the cloned DOM, preserves JSON-LD and page metadata, and records the renderer version, redirects and final public URL. It is not a general-purpose HTML sanitizer and does not flatten iframe/shadow-root contents. HTTP `X-Robots-Tag` and `Content-Language` are retained for later snapshot serving.

Document responses must be HTTP 200 HTML. Private/no-store responses, cookie-setting responses and cookie/authorization-dependent Vary responses are rejected, including redirect headers. Raw response metadata is also checked to catch cookie headers Chrome omits from ordinary response objects. Browser requests do not inherit visitor credentials. Conservatively, private/cookie-setting subresource response metadata can also reject a render. Other status codes are reported as render errors at this stage; removal/tombstone serving is session four.

Browser HTTP(S) traffic is restricted to the origin and explicit `render_allow_origins`, including traffic from worker targets through a local egress guard. Document navigation remains on the configured origin. Cross-origin scripts/fonts/API hosts therefore need explicit permission. Main-tab requests are restricted to GET/HEAD and have cookies/authorization stripped; the guard also strips them from HTTP requests. HTTPS CONNECT traffic is restricted by destination; it is not decrypted by the guard. A fresh browser profile prevents reuse of visitor sessions. For now an app requiring cross-origin iframe navigation or POST-based data loading is outside the supported render path. OS network/container isolation remains part of production deployment, not a claim made by the guard.

The browser loads the configured origin URL directly. Configure the application's canonical/public base URL correctly; origin-host-sensitive applications need special attention because `window.location` reflects that origin. No automatic rewriting of page links or canonical tags is performed. Rendered page content is stored locally, not exposed through a public render endpoint in this checkpoint.

Chrome's sandbox remains enabled by default, including under root. If an isolated container needs it disabled, explicitly set `chrome_no_sandbox` or pass `--chrome-no-sandbox`; the service never silently turns it off. The initial supported/tested storage platform is Linux, where directory fsync is available.

## Configuration

`--config` loads a strict JSON object; unknown fields and files larger than 1 MiB are rejected. CLI flags `--origin`, `--public-url`, `--listen`, `--admin-listen`, `--data-dir`, `--chrome-path`, `--chrome-no-sandbox`, `--ready-selector`, and `--render-timeout` override the corresponding file values. The `RENDERCACHE_ADMIN_TOKEN` environment variable overrides `admin_token`, including when explicitly empty. Configuration and bot lists load at startup; restart to apply changes. Supplying `bots` replaces the default list, and an empty array disables classification.

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
| `listen` | `:8080` | Public proxy listener |
| `admin_listen` | `127.0.0.1:9090` | Administration listener |
| `admin_token` | empty | Optional bearer token on loopback, required elsewhere |
| `origin_timeout` | `30s` | Upstream response-header timeout |
| `shutdown_timeout` | `10s` | Graceful shutdown deadline |
| `bots` | search, AI and preview crawler patterns | Case-insensitive user-agent substring matches |

Every admin route requires `Authorization: Bearer <token>` when a token is configured. Use the environment variable rather than committing a real token. `/healthz` reports process health; `/status` reports routing counters and indicates manual rendering is available while automatic rendering and snapshot serving are disabled. These endpoints are not mounted on the public listener. Public `/status` requests still go to the origin.

Only GET/HEAD document requests without cookies or authorization are eligible for the future snapshot path. API paths, non-HTML file extensions, non-HTML Accept headers, range/upgrade requests and `Cache-Control: no-store` bypass it. A user-agent match is a crawler claim, not an authenticated identity. Every upstream response varies on User-Agent, Cookie and Authorization; upstream cookies, status codes and content are preserved. Incoming forwarding claims are removed before fresh forwarding headers are generated. Keep TLS and any trusted edge routing in front of this HTTP service.

The current JSON stderr events include bot response status and `snapshot_served: false`; they are routing diagnostics, not the persistent, rotating delivery log planned for session five. No render queue is implemented yet; manual `render` always performs new browser work. Bots' query strings are forwarded unchanged, but query strings are omitted from diagnostic logs.

## Roadmap

Session 2 complete: Chromium renderer, readiness/timeout controls, atomic disk snapshots, checksums and restart persistence. Chrome/Chromium is an external dependency; the later container will bundle it.

Session 3: bounded sitemap discovery, scheduled warming, queue deduplication and retry backoff.

Session 4: fresh/stale/cold serving, background refresh, failure preservation and authenticated refresh API.

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
