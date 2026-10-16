// SPDX-License-Identifier: Apache-2.0
package detect

import (
	"golang.org/x/net/html"
	"strings"
	"unicode"
)

type Part struct {
	Text    string `json:"text"`
	Missing bool   `json:"missing"`
}
type Comparison struct {
	RawText       string `json:"raw_text"`
	RenderedText  string `json:"rendered_text"`
	RawWords      int    `json:"raw_words"`
	RenderedWords int    `json:"rendered_words"`
	MissingWords  int    `json:"missing_words"`
	Shell         bool   `json:"likely_shell"`
	Parts         []Part `json:"parts"`
}

// Text approximates visible text, ignoring head/scripts/styles/templates,
// noscript and explicitly hidden subtrees. It does not evaluate CSS or layout.
func Text(source string) string {
	root, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return ""
	}
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "head", "script", "style", "template", "noscript":
				return
			}
			for _, a := range n.Attr {
				if a.Key == "hidden" || (a.Key == "aria-hidden" && strings.EqualFold(a.Val, "true")) {
					return
				}
			}
		}
		if n.Type == html.TextNode {
			out.WriteString(n.Data)
			out.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return strings.Join(strings.Fields(out.String()), " ")
}
func word(s string) string {
	return strings.ToLower(strings.TrimFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }))
}
func Compare(raw, rendered string) Comparison {
	c := Comparison{RawText: Text(raw), RenderedText: Text(rendered), Parts: []Part{}}
	counts := map[string]int{}
	for _, s := range strings.Fields(c.RawText) {
		if w := word(s); w != "" {
			counts[w]++
			c.RawWords++
		}
	}
	for _, s := range strings.Fields(c.RenderedText) {
		w := word(s)
		missing := false
		if w != "" {
			c.RenderedWords++
			if counts[w] > 0 {
				counts[w]--
			} else {
				missing = true
				c.MissingWords++
			}
		}
		c.Parts = append(c.Parts, Part{Text: s, Missing: missing})
	}
	c.Shell = c.RenderedWords >= 10 && c.MissingWords >= 10 && c.RawWords*5 <= c.RenderedWords
	return c
}
