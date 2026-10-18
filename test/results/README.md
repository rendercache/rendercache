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

| Run | darwin arm64 | linux amd64 | windows amd64 |
|---|---|---|---|
| Recorded (UTC) | 2026-10-18T04:36:46Z | 2026-10-18T04:36:04Z | 2026-10-18T04:37:40Z |
| Commit | 6bc0ddbf1e31 | 6bc0ddbf1e31 | 6bc0ddbf1e31 |
| Go | go1.27.2 | go1.27.2 | go1.27.2 |
| Real Chrome | yes | yes | yes |
| CI run | https://github.com/rendercache/rendercache/actions/runs/39097621925 | https://github.com/rendercache/rendercache/actions/runs/39097621925 | https://github.com/rendercache/rendercache/actions/runs/39097621925 |
| Tests passed | 85 | 85 | 85 |
| Tests failed | 0 | 0 | 0 |
| Tests skipped | 1 | 1 | 1 |
| chrome render ms | 6733 | 7881 | 5552 |
| crawler requests while chrome stuck | 100 | 100 | 100 |
| kill corrupt snapshots | 0 | 0 | 0 |
| kill distinct versions seen | 16 | 19 | 9 |
| kill rounds | 60 | 60 | 60 |
| kills during a write | 17 | 4 | 49 |
| off origin requests blocked | 11 | 10 | 9 |
| off origin requests reaching target | 0 | 0 | 0 |
| origin down crawler requests | 50 | 50 | 50 |
| origin down served from saved copy | 50 | 50 | 50 |
| slowest crawler response ms while chrome stuck | 134 | 11 | 2 |
