package detect

import (
	"strings"
	"testing"
)

func TestShellAndExcludedText(t *testing.T) {
	raw := `<html><head><title>hidden metadata</title></head><body><script>one two three four five six seven eight nine ten</script><style>css text</style><template>template text</template><noscript>enable JavaScript please</noscript><span hidden>hidden text</span><span aria-hidden="true">decoration</span><main></main></body></html>`
	rendered := `<h1>A public page worth finding</h1><p>Search crawlers can now read this useful article with all the details.</p>`
	c := Compare(raw, rendered)
	if !c.Shell || c.RawWords != 0 || c.RenderedWords < 10 || !c.Parts[0].Missing {
		t.Fatalf("%+v", c)
	}
	c = Compare(rendered, rendered)
	if c.Shell || c.MissingWords != 0 {
		t.Fatal(c)
	}
	if Compare("", `<h1>Short title</h1>`).Shell {
		t.Fatal("tiny page flagged")
	}
	text := Text(`<div>Hello &amp; welcome <b>to the page</b></div>`)
	if !strings.Contains(text, "Hello & welcome to the page") {
		t.Fatal(text)
	}
}
func TestMissingOccurrences(t *testing.T) {
	c := Compare("<p>hello</p>", "<p>Hello hello world</p>")
	if c.MissingWords != 2 || c.Parts[0].Missing || !c.Parts[1].Missing {
		t.Fatal(c)
	}
}
