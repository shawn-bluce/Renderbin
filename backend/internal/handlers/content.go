package handlers

import (
	"bytes"
	"html"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	ghtml "github.com/yuin/goldmark/renderer/html"
)

// File kinds. A file's kind is fixed at creation and decides how Render turns
// its stored source (files.html_content, which holds the raw source for every
// kind) into the response served at /res/{slug}:
//   - KindHTML     — served verbatim (the original behavior).
//   - KindMarkdown — rendered to HTML with goldmark, wrapped in a styled page.
//   - KindText     — HTML-escaped and shown as preformatted, wrapping text.
//   - KindSite     — a whole directory, stored as a zip in files.archive and
//     served entry by entry under /res/{slug}/{token}/ (see sites.go). Never
//     created through normalizeKind: sites have their own upload endpoints.
const (
	KindHTML     = "html"
	KindMarkdown = "markdown"
	KindText     = "txt"
	KindSite     = "site"
)

// normalizeKind maps a client-supplied kind to a known value. An empty string
// defaults to html so pre-kind API callers keep working; anything unrecognized
// returns ok=false so the handler can 400.
func normalizeKind(k string) (string, bool) {
	switch k {
	case "":
		return KindHTML, true
	case KindHTML, KindMarkdown, KindText:
		return k, true
	default:
		return "", false
	}
}

// extForKind is the download filename extension for a kind.
func extForKind(kind string) string {
	switch kind {
	case KindMarkdown:
		return "md"
	case KindText:
		return "txt"
	case KindSite:
		return "zip"
	default:
		return "html"
	}
}

// downloadContentType is the Content-Type for downloading a kind's raw source
// (the stored source, not the rendered output).
func downloadContentType(kind string) string {
	switch kind {
	case KindMarkdown:
		return "text/markdown; charset=utf-8"
	case KindText:
		return "text/plain; charset=utf-8"
	case KindSite:
		return "application/zip"
	default:
		return htmlContentType
	}
}

// markdown is the shared, stateless goldmark converter. GFM adds tables,
// strikethrough, task lists and autolinks. WithUnsafe passes raw HTML in the
// source through untouched: this app already serves uploader-authored HTML
// files verbatim (contained by the CSP sandbox, see userContentCSP), so
// uploaders are trusted authors and markdown is just another authoring format
// for the same content — sanitizing here would buy no safety the "html" kind
// doesn't already give away.
var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(ghtml.WithUnsafe()),
)

// renderContent turns a file's stored source into the bytes served at
// /res/{slug}, according to its kind.
func renderContent(kind, name, source string) []byte {
	switch kind {
	case KindMarkdown:
		var buf bytes.Buffer
		if err := markdown.Convert([]byte(source), &buf); err != nil {
			// Fall back to showing the raw source as text rather than failing
			// the request on a pathological document.
			return textPage(name, source)
		}
		return documentPage(name, buf.String())
	case KindText:
		return textPage(name, source)
	default: // KindHTML
		return []byte(source)
	}
}

// pageCSS is a small, self-contained stylesheet for the generated markdown/txt
// pages. Inlined (not a CDN link) because the app is meant to run self-hosted
// with no guaranteed internet access.
const pageCSS = `:root{color-scheme:light}
body{margin:0;background:#fff;color:#1f2328;font:16px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif}
.content{max-width:48rem;margin:0 auto;padding:2.5rem 1.25rem 4rem}
.content.text{white-space:pre-wrap;overflow-wrap:anywhere;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:.9rem}
.content>:first-child{margin-top:0}
.content h1,.content h2,.content h3,.content h4{line-height:1.25;margin:1.6em 0 .6em;font-weight:600}
.content h1{font-size:1.9rem;border-bottom:1px solid #d0d7de;padding-bottom:.3em}
.content h2{font-size:1.5rem;border-bottom:1px solid #d0d7de;padding-bottom:.3em}
.content a{color:#0969da}
.content code{background:#eff1f3;padding:.2em .4em;border-radius:6px;font-size:.875em;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.content pre{background:#f6f8fa;padding:1rem;border-radius:8px;overflow:auto}
.content pre code{background:none;padding:0;font-size:.875rem}
.content blockquote{margin:0;padding:0 1em;color:#59636e;border-left:.25em solid #d0d7de}
.content table{border-collapse:collapse}
.content th,.content td{border:1px solid #d0d7de;padding:6px 13px}
.content img{max-width:100%}`

// pageShell wraps a body fragment in a minimal, self-contained HTML document
// with an escaped title.
//
// No lang attribute: the uploader's document could be in any language and we
// have nothing to infer it from, so an honest omission beats declaring every
// page English (which is what a screen reader would then pronounce Chinese
// content as).
func pageShell(title, bodyHTML string) []byte {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString("<title>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</title><style>")
	b.WriteString(pageCSS)
	b.WriteString("</style></head><body>")
	b.WriteString(bodyHTML)
	b.WriteString("</body></html>")
	return []byte(b.String())
}

// documentPage renders already-trusted HTML (goldmark output) inside the shell.
func documentPage(title, bodyHTML string) []byte {
	return pageShell(title, `<article class="content">`+bodyHTML+`</article>`)
}

// textPage renders escaped plain text inside a wrapping <pre>, preserving
// newlines and whitespace while neutralizing any HTML in the source.
func textPage(title, text string) []byte {
	return pageShell(title, `<pre class="content text">`+html.EscapeString(text)+`</pre>`)
}
