# RenderCache

RenderCache is a self-hosted pre-render cache for public JavaScript sites. It renders pages in a separately installed Chrome or Chromium, keeps the finished HTML on disk and serves it to crawlers, while people get the normal app. One Go executable for Linux, macOS and Windows. Apache 2.0.

- Code: https://github.com/rendercache/rendercache (this repository)
- Site: https://github.com/photozeitgeist/rendercache.com (private Hugo site, live at https://rendercache.com)
- Studio: the project is listed on softdevstudio.com; add a short note there after each release.

This file is the one place that says where the next session starts and what comes next. There is no separate roadmap or resume file. Session history up to v0.1.0-alpha.2 is kept in docs/history.md as a record only.

## How the project runs

The project follows the owner's low maintenance workflow: Claude does the code, tests, docs, CI, releases, site and upkeep. The owner gives approvals and does not publish releases. Plans for what comes next are agreed with the owner before they go into the Next section below.

## Product rules

- Render only public, unauthenticated content into shared snapshots. Background renders never inherit visitor cookies or authorization.
- Keep admin routes on the separate admin listener, never on the public one.
- Constrain browser navigation and network access to the origin and explicitly allowed asset origins. Preserve structured data.
- A failed refresh never destroys a good snapshot. Cold behaviour is explicit: a crawler never waits for Chrome.
- Do not claim that a user-agent match verifies a crawler, that serving proves indexing or AI citation, or that every crawler has a fixed timeout.
- Keep the service in Go with Chrome as an external dependency. No speculative abstractions or dependencies.

## Code and tests

- `gofmt`, `go vet` and `go test -race ./...` must pass. Set RENDERCACHE_TEST_CHROME to a Chrome executable to include the real-browser tests (and RENDERCACHE_TEST_NO_SANDBOX=1 only where the sandbox cannot run, such as the Linux CI runner).
- Platform code lives in internal/localfs: Linux and macOS share files_unix.go; Windows has files_windows.go. Snapshot records, the lock file and the delivery log are opened and renamed through localfs.
- `python3 scripts/record-tests.py` runs the suite and writes test/results/<os>-<arch>.json; `--summarize` rebuilds test/results/README.md. CI runs it on Linux, macOS and Windows and prints each platform's results as a `test-results` notice annotation, so results can be copied into test/results from the CI run. Every number on rendercache.com comes from these files. The promise tests are listed in that script.
- Before calling a feature done: an independent review by a separate agent, fixes, then a fresh recording.
- When CI fails, read the job annotations: `gh api repos/rendercache/rendercache/check-runs/JOB_ID/annotations`. Failing tests are annotated with their messages.

## Releases

- To release, raise VERSION (and `version` in cmd/rendercache/main.go and the Dockerfile ARG) on main and update docs/release-notes.md. When CI passes on that commit, .github/workflows/release.yml sees a version with no tag, builds the Linux amd64, macOS arm64 and Windows amd64 archives once on Linux, smoke-tests each on its own system, then tags the commit and publishes "RenderCache vX.Y.Z" with SHA256SUMS. Nothing is published unless every build passes.
- Archive names carry no version, so https://github.com/rendercache/rendercache/releases/latest/download/rendercache_linux_amd64.tar.gz (and the darwin and windows equivalents) always point at the newest stable release. GitHub's latest link skips prereleases (versions with a hyphen).
- Since 0.5.0 the release also builds Linux arm64 and the multi-arch image ghcr.io/rendercache/rendercache (VERSION-amd64/-arm64, then VERSION and latest). deploy/compose.yaml names the image of the release: raise it with VERSION (CI checks). If the image step fails after the release was created, re-run the release run's failed jobs: the image step acts only when vVERSION points at the commit built.
- After a release: check the run, download an archive (amd64 and arm64), verify SHA256SUMS and run the binary; `docker buildx imagetools inspect ghcr.io/rendercache/rendercache:VERSION` lists amd64 and arm64; check an anonymous pull (`curl -s 'https://ghcr.io/token?scope=repository:rendercache/rendercache:pull'` returns a token) before the site tells people to `docker pull`. GitHub creates the package private; an owner makes it public once in its settings. Update rendercache.com where it states the version; add the softdevstudio.com note.

## Site

rendercache.com keeps its Launch template unchanged; edit content and config values only, no public/ folder. Read the site repository's own instructions before editing it.

## Current state

v0.4.0 is the latest release (Linux amd64, macOS arm64, Windows amd64), published on 2026-11-03 by the release workflow from commit 23bb236: open shadow roots and same-origin frames written into saved HTML (`flatten_shadow_roots`, `flatten_frames`), tested with a real Lit 3.3.3 page (internal/render/testdata/lit). v0.3.0 (f5de9d6, same day) added verified crawlers and config reload. test/results holds the recordings from the 0.4.0 release CI run. Both phases had two independent review rounds, all findings fixed with tests (TestReview030*, TestReview040*). The downloaded Linux archives of both were checked against SHA256SUMS and run. rendercache.com has the 0.3.0 and 0.4.0 posts; softdevstudio.com has both notes.

## Next: 0.3 to 0.5, item 7 of softdev-plan.md

Approved by the owner on 2026-11-03 ("Go"): Phases A, B and C and the upkeep of item 7 in softdev-plan.md (photozeitgeist/jollyroger.biz). Each phase is its own release.

Building here: go.mod needs Go 1.26 (the code uses os.Root.Rename; chromedp and x/net need 1.25), and the module proxy is blocked. `apt-get install golang-1.26-go` gives Go 1.26; the four modules are cloned from GitHub into /home/claude/gomods/rc and used through a sandbox-only modfile: `. /home/claude/gomods/rc/env.sh` (recreate it from the versions in go.mod if the session is new). Chrome for Testing is at /opt/google/chrome/chrome.

Phase A, 0.3.0: verified crawlers and reload.
- `verify_crawlers` (default off) / `--verify-crawlers`: a user agent naming a crawler that can be checked must come from an address in its operator's published JSON list or pass forward-confirmed reverse DNS, or it gets the live app (BYPASS, reason `unverified`). internal/verify holds the sources (verify.Builtin, checked on the operators' pages 2026-11-03), list fetching (daily, 15 min retry, last good copy in data_dir/crawlers), DNS checks (2 s, 16 at once, cached 6 h / 30 min).
- `unverifiable_crawlers` (`snapshot` default, or `live`) for crawlers with no published check.
- Client address: the peer, or the last X-Forwarded-For address that isn't a trusted proxy when the peer is trusted or the edge token matched.
- Reload on SIGHUP (not Windows) or admin `POST /reload`: reloadable settings applied atomically (a request keeps the settings it started with), others reported under `restart_required`; an invalid file changes nothing.
- Delivery log field `crawler_check`. Opt-in live test TestLiveRanges and the ranges-live workflow (run by hand).
- Done when: a spoofed GPTBot gets the live app (TestSpoofedCrawlerGetsLiveSite).

Phase B, 0.4.0: flatten open shadow roots and same-origin iframes into the captured HTML. Done when a Lit fixture's captured HTML contains its text.

Phase C, 0.5.0: Linux arm64 archives and a multi-arch image on GHCR.

Upkeep (with Phase A): one README intro, "Run the current checkpoint" renamed, no em dash in the status line.

Steps:
1. [x] Phase A code and tests; README, deployment guide
2. [x] independent review (two rounds), fixes, recording; ranges-live run
3. [x] release 0.3.0 (tag on f5de9d6); site 5b52fc4; softdevstudio note 41baa49; softdev-plan.md
4. [x] Phase B, review (two rounds: 4 defects, then 4), release 0.4.0
5. [ ] Phase C, release 0.5.0
