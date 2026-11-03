RenderCache 0.5.0 runs on Linux arm64 and ships as a container image.

**Linux arm64.** A new archive, `rendercache_linux_arm64.tar.gz`, for ARM servers and cloud instances such as AWS Graviton, Ampere and Raspberry Pi 4 or 5 with a 64-bit system. Google doesn't build Chrome for Linux arm64, so install Chromium from your distribution. The release workflow runs each arm64 archive against Debian's Chromium on an arm64 machine before publishing.

**Container image.** `ghcr.io/rendercache/rendercache` holds RenderCache with Chromium for amd64 and arm64; Docker picks the right one. Tags are the version (`0.5.0`) and `latest`. Each architecture is built natively and run with real Chromium before the release is published, the same way the CI container test runs. The compose example in `deploy/` now uses the published image, and `docker compose build` still builds it from source.

Nothing else changed in the program: the archives hold the same code as 0.4.0 apart from the version. Archives for Linux (amd64, arm64), macOS (Apple silicon) and Windows (amd64), with SHA256SUMS.
