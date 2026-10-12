// Package site turns an uploaded directory into the one zip archive a "site"
// file stores (files.archive), and reads entries back out of it at render time.
//
// It is pure and DB-free, like internal/auth. Every way a site's content gets
// written -- the HTTP zip upload, MCP upload_site and MCP update_site -- goes
// through Normalize, so the rules and limits below are stated once and apply
// per account rather than per protocol.
//
// The limits are what make the storage model safe: every /res request for a
// site reads the whole archive out of SQLite over the server's single
// connection, so the archive has to stay small. "AI-generated sites are mostly
// text" is the expectation; these numbers are the guarantee.
package site

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// MaxTotalBytes caps a site's uncompressed size, matching the single
	// document limit (handlers.maxHTMLBytes).
	MaxTotalBytes = 5 << 20
	// MaxEntries caps how many files a site may hold.
	MaxEntries = 200
	// MaxPathBytes caps one entry's path.
	MaxPathBytes = 255
	// MaxUploadBytes caps the zip request body. Text deflates well, so a site
	// at MaxTotalBytes is normally far smaller than this once compressed; the
	// headroom is for the odd already-compressed asset.
	MaxUploadBytes = 8 << 20
	// IndexFile is the entry every site must have at its root.
	IndexFile = "index.html"

	// maxRawRecords bounds the central directory *before* archive/zip parses
	// it. It is looser than MaxEntries because a real archive also carries
	// directory records and, from macOS, a __MACOSX/._ twin for every file.
	maxRawRecords = 4 * MaxEntries
)

// Entry is one file of a site.
type Entry struct {
	Path string
	Data []byte
}

// EntryInfo describes an entry without its content.
type EntryInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Error is a problem with the uploaded content itself, safe to show the
// uploader. Anything else a function here returns is an internal failure.
type Error struct {
	Msg string
	// TooLarge marks a size or count limit, which HTTP reports as 413.
	TooLarge bool
}

func (e *Error) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...)}
}

func tooLarge(format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...), TooLarge: true}
}

// IsUserError reports whether err describes the uploaded content (and so
// belongs in the response) rather than a server fault.
func IsUserError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// IsTooLarge reports whether err is a size or count limit.
func IsTooLarge(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.TooLarge
}

// ReadZip extracts the files of an uploaded zip. It only decodes; Normalize
// decides whether the result is an acceptable site.
//
// Nothing the archive says about itself is trusted:
//   - the record count is checked before archive/zip parses the central
//     directory, which it turns into one *zip.File per record -- a file that
//     is nothing but directory records would otherwise cost tens of times its
//     own size in memory;
//   - every entry is inflated through a LimitReader sized to what is left of
//     MaxTotalBytes, because the sizes in the headers can be forged.
func ReadZip(b []byte) ([]Entry, error) {
	n, err := countRecords(b)
	if err != nil {
		return nil, err
	}
	if n > maxRawRecords {
		return nil, tooLarge("the zip holds %d records; a site may have at most %d files", n, MaxEntries)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, invalid("not a valid zip file")
	}

	var entries []Entry
	remaining := int64(MaxTotalBytes)
	for _, f := range zr.File {
		if !utf8.ValidString(f.Name) {
			// Older Windows Explorer writes CJK names in the system code page
			// (GBK) without setting the UTF-8 flag. Guessing the encoding would
			// mean a new dependency and still be a guess.
			return nil, invalid("a file name in the zip is not UTF-8 (often a zip made by Windows Explorer with Chinese names); upload the folder directly, or re-create the zip with a tool that writes UTF-8 names")
		}
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if strings.HasSuffix(name, "/") || f.Mode().IsDir() {
			continue
		}
		if isJunk(name) {
			continue
		}
		mode := f.Mode()
		if mode&fs.ModeSymlink != 0 {
			return nil, invalid("%s: symbolic links are not supported", name)
		}
		if !mode.IsRegular() {
			return nil, invalid("%s: only regular files are supported", name)
		}
		if f.Flags&0x1 != 0 {
			return nil, invalid("%s: encrypted zip entries are not supported", name)
		}
		if f.Method != zip.Store && f.Method != zip.Deflate {
			return nil, invalid("%s: unsupported compression method %d (use deflate or store)", name, f.Method)
		}
		data, err := readEntry(f, remaining)
		if err != nil {
			return nil, err
		}
		remaining -= int64(len(data))
		entries = append(entries, Entry{Path: name, Data: data})
	}
	return entries, nil
}

func readEntry(f *zip.File, remaining int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, invalid("%s: %v", f.Name, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, remaining+1))
	if err != nil {
		// A checksum mismatch or a corrupt deflate stream: the content is bad,
		// not the server.
		return nil, invalid("%s: %v", f.Name, err)
	}
	if int64(len(data)) > remaining {
		return nil, tooLarge("the site is larger than %d MB uncompressed", MaxTotalBytes>>20)
	}
	return data, nil
}

// countRecords walks the central directory and counts its records without
// allocating per record. Its only job is to bound what zip.NewReader is about
// to allocate, so it reads just enough structure to count: it does not stop at
// the record count the end-of-directory claims, since that field is 16 bits
// and archive/zip compares it only modulo 65536.
func countRecords(b []byte) (int, error) {
	const (
		eocdSig    = 0x06054b50
		eocdLen    = 22
		cdirSig    = 0x02014b50
		cdirHdrLen = 46
	)
	// The end-of-central-directory record sits in the last 22 bytes plus at
	// most a 64KB comment.
	end := -1
	lo := max(0, len(b)-eocdLen-0xffff)
	for i := len(b) - eocdLen; i >= lo; i-- {
		if binary.LittleEndian.Uint32(b[i:]) == eocdSig {
			end = i
			break
		}
	}
	if end < 0 {
		return 0, invalid("not a valid zip file")
	}
	size := int(binary.LittleEndian.Uint32(b[end+12:]))
	offset := int(binary.LittleEndian.Uint32(b[end+16:]))
	if offset < 0 || size < 0 || offset > end || size > end-offset {
		// Also what a zip64 archive looks like here (0xffffffff), which no
		// site this small needs.
		return 0, invalid("not a valid zip file")
	}
	n := 0
	for p := offset; p < offset+size; {
		if p+cdirHdrLen > offset+size || binary.LittleEndian.Uint32(b[p:]) != cdirSig {
			return 0, invalid("not a valid zip file")
		}
		nameLen := int(binary.LittleEndian.Uint16(b[p+28:]))
		extraLen := int(binary.LittleEndian.Uint16(b[p+30:]))
		commentLen := int(binary.LittleEndian.Uint16(b[p+32:]))
		p += cdirHdrLen + nameLen + extraLen + commentLen
		n++
		if n > maxRawRecords {
			return n, nil
		}
	}
	return n, nil
}

// isJunk reports operating-system litter that should never become part of a
// site: Finder's resource-fork twins and folder metadata, Explorer's
// thumbnail cache and folder settings.
func isJunk(p string) bool {
	segs := strings.Split(p, "/")
	if slices.Contains(segs, "__MACOSX") {
		return true
	}
	base := segs[len(segs)-1]
	return strings.HasPrefix(base, "._") || base == ".DS_Store" ||
		base == "Thumbs.db" || base == "desktop.ini"
}

// Normalize validates a set of entries as a site and returns them in
// canonical form (sorted by path). Besides the checks in validate, it drops
// OS litter and strips a single wrapping directory: zipping a folder "site/"
// typically produces "site/index.html", which should serve at the site root.
func Normalize(entries []Entry) ([]Entry, error) {
	cleaned := make([]Entry, 0, len(entries))
	for _, e := range entries {
		e.Path = strings.ReplaceAll(e.Path, `\`, "/")
		if isJunk(e.Path) {
			continue
		}
		cleaned = append(cleaned, e)
	}
	return validate(stripWrapper(cleaned))
}

// stripWrapper removes one directory level when there is no root index.html
// and every entry lives under the same top-level directory.
func stripWrapper(entries []Entry) []Entry {
	if len(entries) == 0 {
		return entries
	}
	prefix := ""
	for _, e := range entries {
		if e.Path == IndexFile {
			return entries
		}
		top, _, ok := strings.Cut(e.Path, "/")
		if !ok || top == "" {
			return entries
		}
		if prefix == "" {
			prefix = top
		} else if top != prefix {
			return entries
		}
	}
	out := make([]Entry, len(entries))
	for i, e := range entries {
		out[i] = Entry{Path: strings.TrimPrefix(e.Path, prefix+"/"), Data: e.Data}
	}
	return out
}

// validate enforces the shape every stored archive has: safe relative paths,
// no duplicates, no file standing where a directory is needed, a root
// index.html, and the limits.
func validate(entries []Entry) ([]Entry, error) {
	if len(entries) == 0 {
		return nil, invalid("the site has no files")
	}
	if len(entries) > MaxEntries {
		return nil, tooLarge("the site has %d files; at most %d are allowed", len(entries), MaxEntries)
	}
	files := make(map[string]bool, len(entries))
	var total int64
	for _, e := range entries {
		if err := checkPath(e.Path); err != nil {
			return nil, err
		}
		if files[e.Path] {
			return nil, invalid("%s appears more than once", e.Path)
		}
		files[e.Path] = true
		total += int64(len(e.Data))
	}
	if total > MaxTotalBytes {
		return nil, tooLarge("the site is larger than %d MB uncompressed", MaxTotalBytes>>20)
	}
	for p := range files {
		for i := 0; i < len(p); i++ {
			if p[i] == '/' && files[p[:i]] {
				return nil, invalid("%s is both a file and a directory", p[:i])
			}
		}
	}
	if !files[IndexFile] {
		return nil, invalid("the site needs an index.html at its root (file names are case-sensitive)")
	}
	out := make([]Entry, len(entries))
	copy(out, entries)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// checkPath accepts a relative, slash-separated path whose every segment is a
// real name. "." and ".." are refused rather than resolved: a browser resolves
// them in the URL before the request is ever made, so such an entry could
// never be served at the path it claims.
func checkPath(p string) error {
	if p == "" {
		return invalid("an entry has an empty path")
	}
	if len(p) > MaxPathBytes {
		return invalid("%s: paths may be at most %d bytes", p, MaxPathBytes)
	}
	if !utf8.ValidString(p) {
		return invalid("a path is not valid UTF-8")
	}
	if strings.HasPrefix(p, "/") {
		return invalid("%s: absolute paths are not allowed", p)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return invalid("%q: control characters are not allowed in paths", p)
		}
	}
	for seg := range strings.SplitSeq(p, "/") {
		switch seg {
		case "":
			return invalid("%s: empty path segment", p)
		case ".", "..":
			return invalid("%s: '.' and '..' are not allowed in paths", p)
		}
	}
	if len(p) >= 2 && p[1] == ':' {
		return invalid("%s: drive-letter paths are not allowed", p)
	}
	return nil
}

// Pack encodes normalized entries as the archive stored in files.archive.
// The output is deterministic -- entries sorted, no timestamps -- so the same
// content always produces the same bytes.
func Pack(entries []Entry) ([]byte, error) {
	sorted := make([]Entry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range sorted {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: e.Path, Method: zip.Deflate})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(e.Data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Merge applies a patch to a stored site: every path in del must exist and is
// removed, every entry in put is added or overwrites the old one. The result
// goes through the same validation as an upload -- so deleting index.html is
// refused -- but not through wrapper stripping, which would silently move
// every file of an existing site if a patch left them all in one directory.
func Merge(old, put []Entry, del []string) ([]Entry, error) {
	files := make(map[string][]byte, len(old)+len(put))
	for _, e := range old {
		files[e.Path] = e.Data
	}
	putPaths := make(map[string]bool, len(put))
	for _, e := range put {
		putPaths[strings.ReplaceAll(e.Path, `\`, "/")] = true
	}
	for _, p := range del {
		p = strings.ReplaceAll(p, `\`, "/")
		if putPaths[p] {
			return nil, invalid("%s is both written and deleted", p)
		}
		if _, ok := files[p]; !ok {
			return nil, invalid("%s does not exist in this site", p)
		}
		delete(files, p)
	}
	for _, e := range put {
		files[strings.ReplaceAll(e.Path, `\`, "/")] = e.Data
	}
	merged := make([]Entry, 0, len(files))
	for p, data := range files {
		merged = append(merged, Entry{Path: p, Data: data})
	}
	return validate(merged)
}

// Archive is a stored site opened for reading.
type Archive struct {
	files map[string]*zip.File
	order []*zip.File
}

// Open parses a stored archive. Stored archives were produced by Pack, so a
// failure here is data corruption rather than bad input.
func Open(b []byte) (*Archive, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, fmt.Errorf("open site archive: %w", err)
	}
	a := &Archive{files: make(map[string]*zip.File, len(zr.File)), order: zr.File}
	for _, f := range zr.File {
		a.files[f.Name] = f
	}
	return a, nil
}

// Has reports whether the archive holds an entry at path.
func (a *Archive) Has(path string) bool {
	_, ok := a.files[path]
	return ok
}

// Read returns the content of the entry at path; ok is false when there is
// none. Paths match exactly, case included.
func (a *Archive) Read(path string) (data []byte, ok bool, err error) {
	f, ok := a.files[path]
	if !ok {
		return nil, false, nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil, true, err
	}
	defer rc.Close()
	data, err = io.ReadAll(io.LimitReader(rc, MaxTotalBytes+1))
	return data, true, err
}

// List describes every entry from the central directory alone, without
// inflating anything.
func (a *Archive) List() []EntryInfo {
	out := make([]EntryInfo, 0, len(a.order))
	for _, f := range a.order {
		out = append(out, EntryInfo{Path: f.Name, Size: int64(f.UncompressedSize64)})
	}
	return out
}

// Entries inflates every entry, for a patch to be merged onto.
func (a *Archive) Entries() ([]Entry, error) {
	out := make([]Entry, 0, len(a.order))
	for _, f := range a.order {
		data, _, err := a.Read(f.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, Entry{Path: f.Name, Data: data})
	}
	return out, nil
}

// List is Open followed by Archive.List, for callers that only need the
// listing.
func List(b []byte) ([]EntryInfo, error) {
	a, err := Open(b)
	if err != nil {
		return nil, err
	}
	return a.List(), nil
}

// Infos describes entries already in memory.
func Infos(entries []Entry) []EntryInfo {
	out := make([]EntryInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, EntryInfo{Path: e.Path, Size: int64(len(e.Data))})
	}
	return out
}

var titlePattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// Title extracts the <title> of a site's index.html, for naming an upload
// that did not say what it is called. Returns "" when there is none.
func Title(entries []Entry) string {
	for _, e := range entries {
		if e.Path != IndexFile {
			continue
		}
		m := titlePattern.FindSubmatch(e.Data)
		if m == nil || !utf8.Valid(m[1]) {
			return ""
		}
		return strings.Join(strings.Fields(html.UnescapeString(string(m[1]))), " ")
	}
	return ""
}

// contentTypes is spelled out rather than taken from mime.TypeByExtension,
// whose answer depends on the host's /etc/mime.types -- which the alpine
// runtime image does not have. Getting this right matters more than usual:
// every response carries nosniff, so a stylesheet served as
// application/octet-stream is simply not applied.
var contentTypes = map[string]string{
	"html":  "text/html",
	"htm":   "text/html",
	"css":   "text/css",
	"js":    "text/javascript",
	"mjs":   "text/javascript",
	"json":  "application/json",
	"map":   "application/json",
	"md":    "text/plain",
	"txt":   "text/plain",
	"csv":   "text/csv",
	"svg":   "image/svg+xml",
	"xml":   "application/xml",
	"wasm":  "application/wasm",
	"png":   "image/png",
	"jpg":   "image/jpeg",
	"jpeg":  "image/jpeg",
	"gif":   "image/gif",
	"webp":  "image/webp",
	"avif":  "image/avif",
	"ico":   "image/x-icon",
	"woff":  "font/woff",
	"woff2": "font/woff2",
	"ttf":   "font/ttf",
	"otf":   "font/otf",
}

// ContentType picks the Content-Type for an entry. Text types get
// "; charset=utf-8" only when the bytes really are UTF-8: an older page in GBK
// declares its encoding in a <meta> tag, and a charset in the header would
// override it and turn the page into mojibake.
func ContentType(path string, data []byte) string {
	ext := ""
	if i := strings.LastIndexByte(path, '.'); i >= 0 && !strings.Contains(path[i:], "/") {
		ext = strings.ToLower(path[i+1:])
	}
	ct, ok := contentTypes[ext]
	if !ok {
		return "application/octet-stream"
	}
	textual := strings.HasPrefix(ct, "text/") || ct == "application/json" ||
		ct == "application/xml" || ct == "image/svg+xml"
	if textual && utf8.Valid(data) {
		ct += "; charset=utf-8"
	}
	return ct
}

// IsHTML reports whether an entry is an HTML page by its name.
func IsHTML(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".htm")
}
