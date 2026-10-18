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
- After a release: check the run, download an archive, verify SHA256SUMS and run the binary; update rendercache.com where it states the version; add the softdevstudio.com note.

## Site

rendercache.com keeps its Launch template unchanged; edit content and config values only, no public/ folder. Read the site repository's own instructions before editing it.

## Current state

v0.1.0-alpha.2 is the latest release (Linux only). Main now carries macOS and Windows support, per-platform CI, the automated release workflow and recorded tests; these have not been released yet.

## Next

Agreed with the owner on 2026-10-18 when moving RenderCache onto the low maintenance workflow:

1. Get CI green on Linux, macOS and Windows and copy each platform's recorded results into test/results.
2. Independent review of the platform, release and test work; fix the findings and record again.
3. Release 0.2.0 through the automated workflow.
4. rendercache.com: reference page (settings and admin endpoints), a post on how RenderCache was tested (figures from test/results), a post on the name, download links pointing at releases/latest; then a softdevstudio.com note.
