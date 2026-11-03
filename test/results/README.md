# Recorded test results

Written by `scripts/record-tests.py` from full `go test -race` runs. Do not edit by hand; every figure on rendercache.com comes from these files.

| Promise | darwin arm64 | linux amd64 | windows amd64 |
|---|---|---|---|
| Killing the process during a save never leaves a damaged snapshot | passed | passed | passed |
| Concurrent refreshes and reads never see a partial snapshot | passed | passed | passed |
| With the origin down, crawlers still get the saved copy | passed | passed | passed |
| A crawler never waits for Chrome | passed | passed | passed |
| A stale page is served at once and refreshed only once | passed | passed | passed |
| A failed refresh keeps the old copy, and a page that turns private is withdrawn | passed | passed | passed |
| Requests with cookies or credentials never get a shared snapshot | passed | passed | passed |
| Sitemap pages are rendered before the first crawler and survive a restart | passed | passed | passed |
| Chrome captures the finished page and off-origin requests are blocked | passed | passed | passed |
| Data files never follow links | passed | passed | passed |
| Only one process can own a data directory | passed | passed | passed |
| Only a trusted edge can set forwarding headers for the origin | passed | passed | passed |
| With verification on, a request that only claims to be a crawler gets the live site | passed | passed | passed |
| A reload applies new bot rules without a restart, and a broken file changes nothing | passed | passed | passed |
| Text inside web components (Lit) and same-origin frames is in the saved HTML | passed | passed | passed |

| Run | darwin arm64 | linux amd64 | windows amd64 |
|---|---|---|---|
| Recorded (UTC) | 2026-11-03T10:59:15Z | 2026-11-03T10:53:42Z | 2026-11-03T10:54:12Z |
| Commit | 23bb236f18b6 | 23bb236f18b6 | 23bb236f18b6 |
| Go | go1.27.2 | go1.27.2 | go1.27.2 |
| Real Chrome | yes | yes | yes |
| CI run | https://github.com/rendercache/rendercache/actions/runs/41622865798 | https://github.com/rendercache/rendercache/actions/runs/41622865798 | https://github.com/rendercache/rendercache/actions/runs/41622865798 |
| Tests passed | 113 | 113 | 112 |
| Tests failed | 0 | 0 | 0 |
| Tests skipped | 2 | 2 | 2 |
| chrome render ms | 4457 | 794 | 2558 |
| crawler requests while chrome stuck | 100 | 100 | 100 |
| kill corrupt snapshots | 0 | 0 | 0 |
| kill distinct versions seen | 17 | 13 | 9 |
| kill rounds | 60 | 60 | 60 |
| kills during a write | 13 | 3 | 43 |
| off origin requests blocked | 11 | 11 | 11 |
| off origin requests reaching target | 0 | 0 | 0 |
| origin down crawler requests | 50 | 50 | 50 |
| origin down served from saved copy | 50 | 50 | 50 |
| slowest crawler response ms while chrome stuck | 16 | 1 | 2 |
