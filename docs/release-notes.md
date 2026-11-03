RenderCache 0.3.0 can check that a crawler is who it says it is, and reloads its settings without a restart.

**Verified crawlers.** A user agent is a claim anyone can make. With `verify_crawlers` on (or `--verify-crawlers`), a request naming Googlebot, Bingbot, GPTBot, OAI-SearchBot, ChatGPT-User, ClaudeBot, PerplexityBot, Applebot, DuckDuckBot, CCBot and the others whose operators publish a check must come from an address on the operator's published list, or pass a forward-confirmed reverse DNS check (Google, Bing, Apple, Common Crawl, Yandex, Baidu). If it doesn't, it gets the live app, as any visitor would, with `X-RenderCache: BYPASS` and reason `unverified`. The lists are fetched once verification is on, refreshed daily, kept on disk for the next start and dropped after 30 days without a successful refresh. Crawlers with no published check (Meta's, Bytespider, Amazonbot, the Slack, Discord, LinkedIn and X preview bots) keep getting snapshots unless `unverifiable_crawlers` is `live`. Verification is off by default, so upgrading changes nothing until you turn it on.

**Reload.** SIGHUP (not on Windows) or an authenticated `POST /reload` on the admin listener reads the configuration again and applies bot patterns, bypass paths, ignored query parameters, trusted proxies, tokens, `snapshot_ttl` and the verification settings at once. Settings that only take effect at start are listed as `restart_required`. A file that is invalid, alone or together with the running settings, changes nothing.

**Client address.** Behind a trusted proxy or a matching edge token, the client is the last X-Forwarded-For address that isn't a trusted proxy; entries with ports are read too. The nginx and Cloudflare Worker examples already send it.

**Delivery log.** Each receipt has a `crawler_check` field: `off`, `verified`, `unverified`, `pending` (the list is not loaded yet, or too old), `unverifiable` or `simulated`.

**Fixed.** A refresh that saved a page while a crawler was opening the same page could make that read fail as if the file were a link, so the crawler got a cold miss. The open is now retried when the file was replaced in between; a link is still refused.

**Reviewed.** An independent review went through the new code twice before the release, finding four defects and then two small ones. All are fixed with tests, among them a reload that could have removed the admin token while the admin listener was still public, and broad bot patterns such as `bot` that let a spoofed GPTBot through.

Archives for Linux (amd64), macOS (Apple silicon) and Windows (amd64), with SHA256SUMS. Known limits are listed in the README and the deployment guide.
