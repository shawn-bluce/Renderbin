package searchmeta

import (
	"strings"
	"testing"

	"github.com/shawn-bluce/renderbin/backend/internal/site"
)

func TestExtractHTML(t *testing.T) {
	doc := `<!doctype html><html><head>
<meta charset="utf-8">
<title>  Q3 &amp; Q4
  Report </title>
<meta name="description" content="Revenue by region">
<meta property="og:title" content="Q3 &amp; Q4 Report">
<meta property='og:description' content='Shared with the board'>
<META NAME=keywords CONTENT="revenue,growth">
<meta name="author" content="ignored">
</head><body><title>not this one</title><p>Body text is not searched.</p></body></html>`
	got := Extract("html", []byte(doc))
	want := "Q3 & Q4 Report\nRevenue by region\nShared with the board\nrevenue,growth"
	if got != want {
		t.Errorf("Extract(html) =\n%q\nwant\n%q", got, want)
	}
}

func TestExtractHTMLWithoutHead(t *testing.T) {
	if got := Extract("html", []byte("<p>just a fragment</p>")); got != "" {
		t.Errorf("Extract(fragment) = %q, want empty", got)
	}
	// A <title> past the scanned prefix is not looked for.
	far := strings.Repeat("x", scanBytes) + "<title>too far</title>"
	if got := Extract("html", []byte(far)); got != "" {
		t.Errorf("Extract(far title) = %q, want empty", got)
	}
}

func TestExtractMarkdown(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"front matter", "---\ntitle: \"周报 第 42 周\"\ndescription: Weekly notes\ntags: [a, b]\n---\n# Heading\n", "周报 第 42 周\nWeekly notes"},
		{"first heading", "intro line\n\n```\n# not a heading, in a fence\n```\n## Real Title ##\n# later", "Real Title"},
		{"front matter without title falls back", "---\ndescription: only this\n---\n# The Title\n", "The Title\nonly this"},
		{"nothing", "plain text\nno heading", ""},
		{"crlf", "---\r\ntitle: Windows\r\n---\r\n", "Windows"},
	}
	for _, c := range cases {
		if got := Extract("markdown", []byte(c.doc)); got != c.want {
			t.Errorf("%s: Extract = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestExtractSite(t *testing.T) {
	zip, err := site.Pack([]site.Entry{
		{Path: "index.html", Data: []byte(`<head><title>My Site</title><meta name="description" content="docs"></head>`)},
		{Path: "other.html", Data: []byte(`<title>Other</title>`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := Extract("site", zip); got != "My Site\ndocs" {
		t.Errorf("Extract(site) = %q", got)
	}
	if got := Extract("site", []byte("not a zip")); got != "" {
		t.Errorf("Extract(broken site) = %q, want empty", got)
	}
}

func TestExtractTextHasNoFields(t *testing.T) {
	if got := Extract("txt", []byte("# looks like a heading")); got != "" {
		t.Errorf("Extract(txt) = %q, want empty", got)
	}
}

func TestExtractIsBounded(t *testing.T) {
	long := strings.Repeat("长", 500) // 1500 bytes
	doc := `<head><title>` + long + `</title><meta name="description" content="` + long + `x"><meta name="keywords" content="` + long + `y"><meta property="og:description" content="` + long + `z"></head>`
	got := Extract("html", []byte(doc))
	if len(got) > MaxBytes {
		t.Errorf("Extract returned %d bytes, want at most %d", len(got), MaxBytes)
	}
	for _, line := range strings.Split(got, "\n") {
		if len(line) > maxPart || !strings.HasPrefix(line, "长") {
			t.Errorf("part %q: %d bytes, want at most %d and cut on a character boundary", line[:9], len(line), maxPart)
		}
	}
}

func TestExtractDropsInvalidUTF8(t *testing.T) {
	// A GBK page's title is not UTF-8; what survives must be valid text.
	got := Extract("html", []byte("<title>ok \xc4\xe3\xba\xc3</title>"))
	if got != "ok" {
		t.Errorf("Extract(GBK title) = %q, want just the valid part", got)
	}
}
