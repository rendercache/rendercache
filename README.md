# RenderCache

A self-hosted pre-render cache for JavaScript sites, designed to prepare HTML before bots and AI crawlers arrive.

**Prototype in progress — session 1 of 6.** The CLI, reverse proxy, bot classification and private-request bypass work. Rendering, disk snapshots, sitemap warming and the browser demo are not implemented yet. All requests currently reach the origin; recognized public bot document requests are marked `MISS`, not served a snapshot.

## Run the current checkpoint

Requires Go 1.26 or later. Session one uses only the standard library.

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

The first and third requests return `X-RenderCache: BYPASS`; the second returns `X-RenderCache: MISS`. All three contain the same original application shell at this stage. Opening the fixture in a JavaScript-capable browser populates its empty main element from `/api/page`. The `/about` page and `/sitemap.xml` are included for subsequent rendering and warming tests.

## Configuration

`--config` loads a strict JSON object; unknown fields and files larger than 1 MiB are rejected. CLI flags `--origin`, `--listen`, and `--admin-listen` override the corresponding file values. The `RENDERCACHE_ADMIN_TOKEN` environment variable overrides `admin_token`, including when explicitly empty. Configuration and bot lists load at startup; restart to apply changes. Supplying `bots` replaces the default list, and an empty array disables classification.

| Setting | Default | Purpose |
| --- | --- | --- |
| `origin` | required | Fixed upstream site root |
| `listen` | `:8080` | Public proxy listener |
| `admin_listen` | `127.0.0.1:9090` | Administration listener |
| `admin_token` | empty | Optional bearer token on loopback, required elsewhere |
| `origin_timeout` | `30s` | Upstream response-header timeout |
| `shutdown_timeout` | `10s` | Graceful shutdown deadline |
| `bots` | search, AI and preview crawler patterns | Case-insensitive user-agent substring matches |

Every admin route requires `Authorization: Bearer <token>` when a token is configured. Use the environment variable rather than committing a real token. `/healthz` reports process health; `/status` reports routing counters and explicitly indicates that rendering and snapshot serving are disabled. These endpoints are not mounted on the public listener. Public `/status` requests still go to the origin.

Only GET/HEAD document requests without cookies or authorization are eligible for the future snapshot path. API paths, non-HTML file extensions, non-HTML Accept headers, range/upgrade requests and `Cache-Control: no-store` bypass it. A user-agent match is a crawler claim, not an authenticated identity. Every upstream response varies on User-Agent, Cookie and Authorization; upstream cookies, status codes and content are preserved. Incoming forwarding claims are removed before fresh forwarding headers are generated. Keep TLS and any trusted edge routing in front of this HTTP service.

The current JSON stderr events include bot response status and `snapshot_served: false`; they are routing diagnostics, not the persistent, rotating delivery log planned for session five. No render queue is implemented yet. Bots' query strings are forwarded unchanged, but query strings are omitted from diagnostic logs.

## Roadmap

Session 2: Chromium renderer, readiness and timeout controls, atomic disk snapshots and restart persistence. Chrome/Chromium will be an external dependency of the Go binary; the later container will bundle it.

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

Read [RESUME.md](RESUME.md) before continuing development. This checkpoint is foundation code, not a production-ready rendering service.

## License

Apache License 2.0; see [LICENSE](LICENSE).
