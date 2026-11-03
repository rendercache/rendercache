RenderCache 0.5.0 runs on Linux arm64 and ships as a container image.

**Linux arm64.** A new archive, `rendercache_linux_arm64.tar.gz`, for 64-bit ARM Linux servers and cloud instances such as AWS Graviton or Ampere; it is tested on GitHub's arm64 machines. Google doesn't build Chrome for Linux arm64, so install Chromium from your distribution. The release workflow runs each arm64 archive against Debian's Chromium on an arm64 machine before publishing.

**Container image.** `ghcr.io/rendercache/rendercache` holds RenderCache with Chromium for amd64 and arm64; Docker picks the right one. Tags are the version (`0.5.0`) and `latest`. Each architecture is built natively and run with real Chromium before the release is published, the same way the CI container test runs. The image is now based on Debian 13 (trixie), whose Chromium gets full security support; Debian 12 moved to LTS in July. The compose example in `deploy/` names the image of its release, and `docker compose up --build` still builds it from source.

Nothing else changed in the program: the archives hold the same code as 0.4.0 apart from the version. Archives for Linux (amd64, arm64), macOS (Apple silicon) and Windows (amd64), with SHA256SUMS.
