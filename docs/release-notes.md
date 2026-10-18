RenderCache 0.2.0 runs on Linux, macOS and Windows, and is the first release built and published entirely by the automated release workflow.

**Downloads.** Archives for Linux (amd64), macOS (Apple silicon) and Windows (amd64), with SHA256SUMS. The archive names carry no version, so `https://github.com/rendercache/rendercache/releases/latest/download/rendercache_linux_amd64.tar.gz` (and the `darwin_arm64.tar.gz` and `windows_amd64.zip` equivalents) always fetch the newest stable release. Chrome or Chromium is installed separately; the Dockerfile includes it.

**macOS and Windows.** The data directory works the same on all three: files are opened without following links, one process owns the directory through a lock, and a snapshot is only ever replaced by a complete copy. On Windows a refresh can replace a snapshot while a crawler is still reading it. Windows can't sync a directory, so after a power failure the most recent refresh may be lost; the previous snapshot stays intact.

**Links inside the data directory.** Earlier releases refused a link that pointed outside the data directory but would follow one that stayed inside it. Every link is now refused.

**Tested on every platform.** CI runs the full race-enabled test suite with real Chrome on Linux, macOS and Windows, then builds and smoke-tests the executable on each. The results are recorded in `test/results`, including new tests that kill the process in the middle of a save, take the origin down, and leave Chrome stuck, to check that crawlers still get a complete saved page without waiting.

**Request-driven pages.** When all `request_page_limit` slots are used, a new URL takes the slot of the least recently requested idle page, but only one nobody has asked for in the last hour. The evicted page's snapshot is deleted. So crawler traffic can render at most `request_page_limit` new pages an hour, and request-driven snapshots never take more disk than the allowance.

**Settings.** Purge and compare now drop the `ignore_query_params` parameters too, so they act on the same snapshot a crawler gets. The container example no longer trusts the whole Docker address range for forwarding headers; add the exact gateway address, or use an edge token.

Since 0.1.0-alpha.2 is a prerelease, this is also the first version the `releases/latest` links point to. This is an early release for evaluation; known limits are listed in the README and the deployment guide.
