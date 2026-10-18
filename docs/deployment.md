# RenderCache deployment

Release archives cover Linux amd64, macOS arm64 and Windows amd64. Install a maintained Chrome/Chromium separately for the standalone binary, or build the supplied Dockerfile. Secure file opening, directory locking and atomic snapshot replacement work on all three. Windows cannot sync a directory, so a power failure there may lose the most recent refresh (the previous snapshot stays intact). Do not treat the browser network guard as an OS sandbox. Keep your app's public/canonical URL configured correctly: the browser renders the fixed origin directly and sees its hostname.

## Standalone service

Verify `sha256sum -c SHA256SUMS` from the download directory, then extract the binary archive and copy/edit the sample JSON. Run as an unprivileged dedicated OS user. Give that user exclusive write access to its local data directory. Set `RENDERCACHE_ADMIN_TOKEN` through your service's secret/environment facility; avoid putting secrets in the JSON checked into Git. Run:

```sh
rendercache serve --config /etc/rendercache.json
rendercache health
```

Set `chrome_path` to the actual executable and configure a real readiness selector. The public listener defaults to port 8080; admin defaults to 127.0.0.1:9090. Neither listener provides TLS: use an existing HTTPS edge or a secure tunnel. Do not map admin paths through the public edge. `health` uses `RENDERCACHE_ADMIN_TOKEN` and a two-second deadline, does not follow redirects and defaults to the loopback admin health URL. Override with `--admin-url` for a different health address. If your token lives only in JSON, the health command still needs it supplied via environment.

`rendercache render` and `prune` require the same data-directory lock as `serve`; stop the service before running either. Use the live authenticated purge API to request refreshes without stopping service. `inspect` is read-only and can read atomic snapshots while the service runs. Do not modify managed files externally while it runs. Network filesystems and multi-process/shared-volume operation are unsupported.

## Quotas and maintenance

Defaults are 1 GiB of serialized snapshot records and 10,000 record files, separate from the current delivery log plus its rotated backups. `snapshot_max_bytes` accepts 4096 bytes through 1 TiB; `snapshot_max_files` accepts 1 through 100,000. A rejected write preserves an existing snapshot and is visible in `/status` under `storage.rejected_writes`; warm retry/error status is also visible. There is no silent eviction. A smaller non-growing replacement is allowed when old records exceed a newly lowered quota. Existing records remain readable after lowering limits.

To reclaim records by filesystem modification age, stop the service and run:

```sh
rendercache prune --config /etc/rendercache.json --older-than 720h
```

The minimum age is one minute. Prune removes recognized regular `<64 lowercase hex>.json` files older than the cutoff, including corrupt records. It leaves logs, unknown files and newer records. It does not know whether an old page is still in the sitemap; it is an explicit operator decision and warming can recreate it after restart. Startup (for writable commands holding the lock) removes abandoned temporary files with the exact current snapshot temp-name format; it never follows symlinks or opens named pipes as cache/log/lock files. Older pre-alpha temp formats and unknown files need operator review. Restart reconstructs counts from disk, counting corrupt records too. Directory scans cap at 200,000 total entries and 100,000 record names; an oversized/misused directory needs offline repair.

The record quota counts serialized JSON rather than HTML length. Atomic replacement can temporarily require one additional complete record while the old file remains present; the encoder also needs memory. Delivery logs and unrelated files are outside the snapshot quota. Use filesystem/container disk limits as a final ceiling. Snapshots use file and directory fsync on Linux; delivery receipts use best-effort writes and rotation. Crash recovery may leave an incomplete final log line. Do not mistake a successful write receipt for proof of indexing or a client read acknowledgement.

## Docker

From the tagged source checkout:

```sh
docker build -t rendercache:local .
export RENDERCACHE_ADMIN_TOKEN='replace-with-a-long-secret'
# Edit deploy/container.json: origin must be reachable from the container,
# public_url is your site's public root, and ready_selector must fit your app.
docker compose -f deploy/compose.yaml up -d
```

The image uses UID/GID 10001, Chromium, its sandbox helper and tini. Compose restricts CPU, memory, PIDs, the root filesystem and writable temp space, drops capabilities and binds only the public port to host loopback. Only `/data` persists; `/tmp` is a capped tmpfs. An `origin` DNS name is a placeholder for your app on a shared Docker network: attach the service to that network or change origin to a reachable address. Host loopback from inside the container is the container itself. Configure volume ownership for UID 10001 if using a bind mount rather than the supplied named volume. Admin remains reachable only from inside the container network namespace (for example through a carefully configured SSH/container namespace tunnel); it is not published by compose.

Chrome's sandbox stays enabled by default. Linux user namespaces, seccomp and host LSM/AppArmor policy must permit it. A sandbox initialization failure is a failed render, not an automatic downgrade. Verify it on your deployment host before serving traffic. The CI runtime smoke uses a dedicated internal Docker network, a non-root user, no-new-privileges, dropped capabilities, a read-only root filesystem and resource limits, with `--chrome-no-sandbox` explicitly added for that isolated test. That test proves packaging/rendering under those restrictions; it does not prove sandbox operation on your host. Do not solve sandbox problems by routinely granting privileged/SYS_ADMIN access. An explicit sandbox opt-out belongs only in an independently isolated environment you control.

A 1 GiB memory limit is an example, not a guarantee that every page fits. Chrome's processes and temporary browser profile consume additional memory/temp disk, and can be killed by the container limits. The public/admin listeners admit at most 256/16 open connections, with header/read/idle deadlines. Public request concurrency defaults to 32 (configurable 1..256), and each browser egress proxy admits 64 connections. Origin response headers cap at 64 KiB. The application limits browser jobs, HTML size, diagnostic concurrency and HTTP work; it does not enforce a per-render RSS limit outside a container/cgroup. Tune memory/CPU/PIDs/tmpfs and `render_concurrency` together. Browser asset egress must reach only intended upstreams; apply host/network firewall policy as appropriate. Rebuild the image regularly for browser and OS security updates. OS packages/base layers can change: the deterministic guarantee applies to same-source/same-toolchain binary archives, not bit-for-bit Docker image builds.

## nginx

`deploy/nginx.conf` is a complete local example, validated in CI and exercised against real rendered snapshots. Replace the origin/cache upstream addresses and server name, then add your normal TLS termination. The example listens on host loopback port 8088; do not mistake it for a public TLS configuration. It routes all matching crawler user agents to RenderCache, so Go can record private/API/asset bypasses and preserve origin behavior. Humans go directly to the app. The backend remains on 127.0.0.1:8080, and nginx never exposes port 9090. Keep crawler patterns aligned with your Go config. User-agent matching is a routing hint, not authentication.

The nginx example reaches RenderCache over loopback, which is in the default `trusted_proxies`, so the origin receives the visitor's address and the public `https` scheme from nginx's X-Forwarded-* headers. If nginx runs on another host, or reaches RenderCache in Docker through the published port (the container then sees the Docker bridge gateway, not loopback), add that exact address (for example the bridge gateway, often 172.17.0.1/32) to `trusted_proxies`. Trust only addresses that belong to your own edge: any trusted peer can set these headers for the origin, and with Docker's userland proxy every outside client can appear as the gateway, so in that setup use an edge token instead.

Do not set `origin` to this nginx frontend: it must point to the original app, otherwise rendering loops. URI escaping/query strings are preserved, proxy cache is off and redirect rewriting is disabled. Any additional CDN/edge cache must bypass these HTML routes or implement correct bot/human variation; do not rely on arbitrary `Vary: User-Agent` support at a CDN. The example clears any visitor edge-key header and uses a private backend, so it needs no edge token. If you change that topology to an external backend, use a properly configured TLS/authenticated route rather than sending a secret to the human app upstream.

## Cloudflare Worker routing

`deploy/cloudflare/worker.mjs` is a routing example, not a renderer. Its request logic is unit-tested; live Cloudflare deployment is intentionally left to the operator. Use a Worker Route in front of the existing application and a separate HTTPS backend hostname that routes only to RenderCache's public listener. Do not route that backend hostname back through this Worker. Do not use RenderCache.com as an origin; the project site is separate static hosting.

In wrangler.toml edit `PUBLIC_HOST`, `CACHE_ORIGIN` and your commented example Route/zone. Store `RENDERCACHE_EDGE_TOKEN` as a Worker secret and configure the same value in the Go backend environment. Go requires `X-RenderCache-Edge-Key` on its public listener when that secret is configured and strips the header before forwarding to the origin. The Worker overwrites any incoming value; it never follows backend redirects. This is shared-secret access control over TLS, not verified crawler authentication. Rotate the value in both places together and keep it out of source/logs. Keep the admin listener private and protect/rate-limit the backend at the network edge too.

The Worker sends matching GET/HEAD crawler requests without credentials, cookies, ranges or protocol upgrades to the backend. Other requests retain application-origin behavior and are not visible in RenderCache's delivery log. For complete bypass receipts use the full reverse-proxy deployment or the nginx example. The Go backend remains the final authority on HTML eligibility. Saved snapshots and cold fallback responses from the bot route are marked private/no-store, and Worker subrequest caching is disabled. The Worker sends the visitor's `CF-Connecting-IP`, the public scheme and host as X-Forwarded-For/Proto/Host. RenderCache passes them to the origin because the edge token proves the request came through the Worker. Configure any overriding zone Cache Rules to bypass these HTML/backend routes; rules can supersede application intent. Backend fetches have a ten-second deadline and explicit failure responses. No script, domain, DNS, Worker or Cloudflare rule is deployed automatically by this repository.

References used for the examples: nginx map/proxy modules (`https://nginx.org/en/docs/http/ngx_http_map_module.html`, `https://nginx.org/en/docs/http/ngx_http_proxy_module.html`); Cloudflare Worker Routes/Request API (`https://developers.cloudflare.com/workers/configuration/routing/routes/`, `https://developers.cloudflare.com/workers/runtime-apis/request/`); Docker resource limits/tmpfs (`https://docs.docker.com/engine/containers/resource_constraints/`, `https://docs.docker.com/engine/storage/tmpfs/`).

## Release verification

`bash scripts/build-release.sh dist [GOOS GOARCH]` writes one deterministic archive (`rendercache_<os>_<arch>.tar.gz`, or `.zip` for Windows) and its SHA256SUMS line under `dist`. CI builds the Linux, macOS and Windows archives twice from identical source with Go 1.27.2 and compares them, and exercises the extracted Linux binary with a real browser, Docker/Chromium and nginx. Go dependencies are checksum-pinned.

Releases are published by `.github/workflows/release.yml`. After CI passes on a push to main whose VERSION has no tag yet, it builds all three archives once on Linux (twice, comparing checksums), runs each extracted executable against real Chrome on its own system, then tags the commit and publishes exactly those files with SHA256SUMS. Archive names carry no version, so `releases/latest/download/<archive>` links always fetch the newest stable release; prereleases are only on the releases page.
