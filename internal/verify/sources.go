// SPDX-License-Identifier: Apache-2.0
package verify

// Source says how one crawler can be checked: by the IP ranges its operator
// publishes, by forward-confirmed reverse DNS, or both. A crawler without a
// source cannot be verified, only claimed by its user agent.
type Source struct {
	Crawler     string   // the bot name as it appears in the bots list
	RangesURL   string   // published JSON list of prefixes ({"prefixes":[{"ipv4Prefix":...}]})
	DNSSuffixes []string // reverse DNS names must end in one of these, and resolve back
}

// Builtin lists the methods the operators document. Lists that several
// agents share (Anthropic publishes one for all of its bots) are fetched once.
// Sources checked 2026-11-03 on each operator's own page.
var Builtin = []Source{
	{Crawler: "Googlebot", RangesURL: googleCommon, DNSSuffixes: []string{"googlebot.com", "google.com"}},
	{Crawler: "Google-InspectionTool", RangesURL: googleCommon, DNSSuffixes: []string{"googlebot.com", "google.com"}},
	{Crawler: "GoogleOther", RangesURL: googleCommon, DNSSuffixes: []string{"googlebot.com", "google.com"}},
	{Crawler: "bingbot", RangesURL: "https://www.bing.com/toolbox/bingbot.json", DNSSuffixes: []string{"search.msn.com"}},
	{Crawler: "DuckDuckBot", RangesURL: "https://duckduckgo.com/duckduckbot.json"},
	{Crawler: "OAI-SearchBot", RangesURL: "https://openai.com/searchbot.json"},
	{Crawler: "GPTBot", RangesURL: "https://openai.com/gptbot.json"},
	{Crawler: "ChatGPT-User", RangesURL: "https://openai.com/chatgpt-user.json"},
	{Crawler: "ClaudeBot", RangesURL: anthropic},
	{Crawler: "Claude-SearchBot", RangesURL: anthropic},
	{Crawler: "Claude-User", RangesURL: anthropic},
	{Crawler: "PerplexityBot", RangesURL: "https://www.perplexity.ai/perplexitybot.json"},
	{Crawler: "Perplexity-User", RangesURL: "https://www.perplexity.ai/perplexity-user.json"},
	{Crawler: "Applebot", RangesURL: "https://search.developer.apple.com/applebot.json", DNSSuffixes: []string{"applebot.apple.com"}},
	{Crawler: "CCBot", RangesURL: "https://index.commoncrawl.org/ccbot.json", DNSSuffixes: []string{"crawl.commoncrawl.org"}},
	{Crawler: "YandexBot", DNSSuffixes: []string{"yandex.ru", "yandex.net", "yandex.com"}},
	{Crawler: "Baiduspider", DNSSuffixes: []string{"baidu.com", "baidu.jp"}},
}

const (
	googleCommon = "https://developers.google.com/static/crawling/ipranges/common-crawlers.json"
	anthropic    = "https://claude.com/crawling/bots.json"
)
