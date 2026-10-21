// Package searchmeta extracts the few fields a file is found by: an HTML
// document's <title>, description, keywords and Open Graph title/description;
// a Markdown document's front-matter title and description, or else its first
// heading. Pure and DB-free, like internal/site.
//
// The default search looks at a file's name and these fields only, in SQL, so
// it never has to read a document. The fields are computed once, when content
// is stored, and kept with the blob (blobs.search_meta): they are derived from
// the bytes alone, and a blob never changes.
package searchmeta

import (
	"bytes"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/shawn-bluce/renderbin/backend/internal/site"
)

const (
	// MaxBytes bounds what one file stores. A field longer than maxPart is
	// cut rather than dropped.
	MaxBytes = 1024
	maxPart  = 300
	// scanBytes is how much of a document is looked at. <head> is at the top,
	// and a front matter block or a first heading is too; scanning a 5MB
	// document for a <title> it does not have would buy nothing.
	scanBytes = 64 << 10
)

var (
	headEnd      = regexp.MustCompile(`(?i)</head\s*>`)
	titleTag     = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	metaTag      = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	attr         = regexp.MustCompile(`(?is)([a-z][a-z0-9:_-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	anyTag       = regexp.MustCompile(`(?s)<[^>]*>`)
	atxHeading   = regexp.MustCompile(`^ {0,3}#{1,6}[ \t]+(.+?)[ \t]*#*[ \t]*$`)
	frontMatterK = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)\s*:\s*(.*)$`)
)

// metaFields are the <meta> tags kept, by their name= or property=, in the
// order they are stored after <title>.
var metaFields = []string{"og:title", "description", "og:description", "keywords"}

// Extract returns the searchable fields of content stored as kind ("html",
// "markdown", "txt" or "site"), one per line, or "" when there are none.
func Extract(kind string, content []byte) string {
	var parts []string
	switch kind {
	case "html":
		parts = fromHTML(content)
	case "markdown":
		parts = fromMarkdown(content)
	case "site":
		parts = fromSite(content)
	}
	return join(parts)
}

func head(content []byte) []byte {
	if len(content) > scanBytes {
		content = content[:scanBytes]
	}
	return content
}

func fromHTML(content []byte) []string {
	doc := head(content)
	if loc := headEnd.FindIndex(doc); loc != nil {
		doc = doc[:loc[0]]
	}
	var parts []string
	if m := titleTag.FindSubmatch(doc); m != nil {
		parts = append(parts, string(anyTag.ReplaceAll(m[1], nil)))
	}
	found := map[string]string{}
	for _, tag := range metaTag.FindAll(doc, -1) {
		var key, value string
		hasContent := false
		for _, a := range attr.FindAllSubmatch(tag, -1) {
			v := string(a[2]) + string(a[3]) + string(a[4])
			switch strings.ToLower(string(a[1])) {
			case "name", "property":
				key = strings.ToLower(strings.TrimSpace(v))
			case "content":
				value, hasContent = v, true
			}
		}
		if hasContent && key != "" {
			if _, seen := found[key]; !seen {
				found[key] = value
			}
		}
	}
	for _, k := range metaFields {
		if v, ok := found[k]; ok {
			parts = append(parts, v)
		}
	}
	return parts
}

// fromMarkdown reads a YAML front matter block's title and description (plain
// "key: value" lines; anything fancier is ignored rather than parsed), and
// falls back to the first ATX heading outside a code fence for the title.
func fromMarkdown(content []byte) []string {
	lines := strings.Split(strings.ReplaceAll(string(head(content)), "\r\n", "\n"), "\n")
	var title, description string
	body := lines
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			l := strings.TrimSpace(lines[i])
			if l == "---" || l == "..." {
				body = lines[i+1:]
				for _, fm := range lines[1:i] {
					m := frontMatterK.FindStringSubmatch(fm)
					if m == nil {
						continue
					}
					v := unquote(strings.TrimSpace(m[2]))
					switch strings.ToLower(m[1]) {
					case "title":
						title = v
					case "description":
						description = v
					}
				}
				break
			}
		}
	}
	if title == "" {
		fenced := false
		for _, l := range body {
			trimmed := strings.TrimSpace(l)
			if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
				fenced = !fenced
				continue
			}
			if m := atxHeading.FindStringSubmatch(l); m != nil && !fenced {
				title = m[1]
				break
			}
		}
	}
	return []string{title, description}
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// fromSite reads the site's index.html. A site that cannot be opened has no
// fields rather than an error: search metadata is never a reason to refuse
// content that was valid enough to store.
func fromSite(content []byte) []string {
	arc, err := site.OpenBytes(content)
	if err != nil {
		return nil
	}
	index, ok, err := arc.Read(site.IndexFile)
	if err != nil || !ok {
		return nil
	}
	return fromHTML(index)
}

// join cleans each part (entities decoded, whitespace collapsed, invalid UTF-8
// dropped, cut at maxPart), drops empty and repeated ones (og:title usually
// repeats <title>), and stops at MaxBytes.
func join(parts []string) string {
	var out []string
	seen := map[string]bool{}
	total := 0
	for _, p := range parts {
		p = strings.ToValidUTF8(p, "")
		p = strings.Join(strings.Fields(html.UnescapeString(p)), " ")
		p = cut(p, maxPart)
		key := strings.ToLower(p)
		if p == "" || seen[key] {
			continue
		}
		if total+len(p)+1 > MaxBytes {
			break
		}
		seen[key] = true
		out = append(out, p)
		total += len(p) + 1
	}
	return strings.Join(out, "\n")
}

// cut shortens s to at most n bytes without splitting a character.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	b := []byte(s[:n])
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(bytes.TrimSpace(b))
}
