# RenderCache — resume instructions

Updated: 2026-10-16. Current checkpoint: session 1 published; session 2 in progress, authorized by the user's Go command.

## User authorization and workflow

The user authorized starting the six-session prototype and requires a pause, report and updated resume instructions after each session. Sessions are scoped at approximately 2–3 hours; continue only when the user commands. The next session is session 2. Do not proceed to it automatically.

Engine: https://github.com/rendercache/rendercache (public, Apache 2.0).
Website: https://github.com/photozeitgeist/rendercache.com (private; read access confirmed and account-level write permission reported on 2026-10-16; existing Hugo template/content). Project domain: RenderCache.com. User will connect the domain later. No website files or domain settings changed in session 1. Inspect that repository's instructions and existing template before any future website changes. This website URL was supplied while session 1 was in progress and does not cancel the engine task.

## Agreed product

Self-hosted pre-render cache for public JavaScript apps. Human visitors receive the origin app; recognized crawler requests receive prepared HTML once snapshot serving exists. Pre-warm sitemap URLs on a schedule. Serve stale snapshots during background refresh. Provide shell detection, honest bot delivery logs and one status/demo page. One Go executable plus externally installed Chrome/Chromium; later provide a bundled Docker image. Apache 2.0 for engine. Do not implement PDFs, hosted billing, a large dashboard or framework plugins. Define the renderer interface in session 2; defer Lightpanda and BareProxy integration.

Cold miss: immediately proxy origin and queue a render once queueing exists. No synchronous browser start on a bot request. Expired hit: serve saved snapshot and deduplicate refresh work. Refresh API: preserve existing usable copy until successful replacement. Confirmed removal responses need explicit handling so deleted pages do not remain successful cached pages indefinitely.

## Session 1 delivered

Go module and serve/version/help commands. Strict JSON config, CLI overrides and admin-token environment override. Fixed-origin reverse proxy with preserved paths/query strings and fresh forwarding headers. Editable case-insensitive bot patterns. Public/private request classification. Explicit BYPASS/MISS response headers and Vary handling. Separate admin listener with health/status JSON and optional bearer token (required outside loopback). Structured routing diagnostics. SIGINT/SIGTERM graceful shutdown and startup failure cleanup. Standard-library-only JavaScript fixture with API, about route and sitemap. Apache 2.0 license, README, AGENTS.md and CI workflow.

No Chrome rendering, snapshots, render queue, persistent bot log, purge endpoint or browser demo exists yet. Status reports these limitations. Do not describe this as a finished pre-render cache or publish a release announcing completed rendering.

## Verification

Verified locally on Linux amd64 with Go 1.27.2 (official archive, SHA-256 checked). go test -race ./... passed all four packages; go vet ./... passed; go build produced the engine and fixture executables; gofmt and git diff --check were clean. Live subprocess HTTP smoke passed human BYPASS, GPTBot MISS, cookie-bearing bot BYPASS, API passthrough, unchanged raw fixture shell, admin isolation/counters, 502 on unavailable origin and successful SIGTERM shutdown. A CI workflow for formatting, vet, race tests and build with Go 1.26.x is included, but has not run remotely because publication was rejected. No browser rendering checks apply yet because rendering is session 2.

An initial standalone build encountered parent-workspace VCS metadata rather than a source error; initializing a dedicated local git repository resolved it. A temporary syntax error during an Accept-header quality-value improvement was fixed before the final passing checks. No known failing tests remain. No release tag or binary release was published for this foundation checkpoint.

## Publication history — resolved

Initial GitHub create-file attempt on rendercache/rendercache returned HTTP 403, Resource not accessible by integration. Access was repaired during session 2 on 2026-10-16: the rendercache app installation is now present and a real README write succeeded. Repository metadata reports the account has push/admin rights, but the integration installations list contains no rendercache organization. The existing public repo is readable without an installation, which caused the earlier access check to overstate write access. User must install/authorize the connected GitHub app for the rendercache organization and repository. No remote code or README was written. Do not claim pushed code or successful CI.

The local repo is committed. A source ZIP plus a Git bundle preserves the exact checkpoint while access is repaired. On the next command, check access and publish session 1 first. Fetch current remote contents to avoid overwriting intervening changes; do not force-push. If main remains empty, initialize via an authorized contents write and upload the rest with GitHub Git-tree/commit APIs, or use standard authenticated git push when available. Verify the remote tree and CI results. Then continue session 2 only if the user's command authorizes it.

## Next coding session: session 2

First read this file and AGENTS.md, fetch current main and check for intervening changes. Run baseline checks. Implement a renderer interface and Chrome implementation, readiness selector/settle timeout and hard timeout; capture HTML/status/metadata, preserve JSON-LD, remove executable scripts. Add atomic disk snapshot storage and metadata, site-plus-URL keying and restart persistence. Explicitly map public site URLs to configured origin without looping through the proxy. Constrain browser destinations, redirects and subresources; never render using visitor credentials. Add real-browser fixture checks and storage failure tests. Integrate only the session-2 acceptance surface; scheduled warming and the full stale/cold request lifecycle are sessions 3/4. Update this file, commit/push, report and pause.

## Remaining sessions

3: sitemap URL sets/indexes/gzip, discovery limits, bounded deduplicated warm queue, schedule/backoff and coverage API.
4: snapshot serving lifecycle, duplicate refresh suppression, failure preservation, removal semantics and authenticated refresh endpoint.
5: shell detector, rotating delivery log, protected status page and raw/rendered comparison with missing text and simulated bot request.
6: end-to-end checks, runtime/storage limits, Docker, nginx/Cloudflare deployment instructions and tested release binaries/checksums.

## Local reproduction

From repository root: go test -race ./...; go vet ./...; go build -o bin/rendercache ./cmd/rendercache. Run go run ./examples/spa, then bin/rendercache serve --config rendercache.example.json. Human response should be BYPASS, GPTBot response MISS, cookie-bearing GPTBot response BYPASS. All return origin shell at session 1. Local Go installation for this session is /tmp/rendercache-toolchain/go/bin/go; do not assume this temporary toolchain survives a new environment.
