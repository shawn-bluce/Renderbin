package site

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"io/fs"
	"strings"
	"testing"
)

type zipFile struct {
	name   string
	data   string
	method uint16
	mode   fs.FileMode
	flags  uint16
	raw    bool // write name bytes without the UTF-8 flag
}

func makeZip(t *testing.T, files ...zipFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: f.method}
		if f.mode != 0 {
			h.SetMode(f.mode)
		}
		h.Flags |= f.flags
		if f.raw {
			h.NonUTF8 = true
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatalf("CreateHeader(%q): %v", f.name, err)
		}
		if _, err := w.Write([]byte(f.data)); err != nil {
			t.Fatalf("write %q: %v", f.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func paths(entries []Entry) string {
	var p []string
	for _, e := range entries {
		p = append(p, e.Path)
	}
	return strings.Join(p, ",")
}

func readAndNormalize(t *testing.T, b []byte) ([]Entry, error) {
	t.Helper()
	entries, err := ReadZip(b)
	if err != nil {
		return nil, err
	}
	return Normalize(entries)
}

func TestReadZipHappyPath(t *testing.T) {
	b := makeZip(t,
		zipFile{name: "site/", mode: fs.ModeDir | 0o755},
		zipFile{name: "site/index.html", data: "<title>Hi</title>", method: zip.Deflate},
		zipFile{name: "site/css/a.css", data: "body{}"},
		zipFile{name: "__MACOSX/site/._index.html", data: "junk"},
		zipFile{name: "site/.DS_Store", data: "junk"},
		zipFile{name: "site/中文.html", data: "<p>ok</p>"},
	)
	got, err := readAndNormalize(t, b)
	if err != nil {
		t.Fatalf("readAndNormalize: %v", err)
	}
	if want := "css/a.css,index.html,中文.html"; paths(got) != want {
		t.Errorf("paths = %q, want %q", paths(got), want)
	}
	if Title(got) != "Hi" {
		t.Errorf("Title = %q, want Hi", Title(got))
	}
}

func TestReadZipRejections(t *testing.T) {
	cases := []struct {
		name  string
		files []zipFile
		want  string
	}{
		{"symlink", []zipFile{{name: "index.html", data: "x"}, {name: "l", data: "/etc/passwd", mode: fs.ModeSymlink | 0o777}}, "symbolic links"},
		{"encrypted", []zipFile{{name: "index.html", data: "x", flags: 0x1}}, "encrypted"},
		{"non-utf8 name", []zipFile{{name: "\xd6\xd0\xce\xc4.html", data: "x", raw: true}}, "not UTF-8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ReadZip(makeZip(t, c.files...))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
			if !IsUserError(err) {
				t.Errorf("err %v should be a user error", err)
			}
		})
	}

	t.Run("unsupported method", func(t *testing.T) {
		b := makeZip(t, zipFile{name: "index.html", data: "x"})
		// Rewrite the method in both the local header (offset 8) and the
		// central directory record (offset 10) to 12 (bzip2).
		binary.LittleEndian.PutUint16(b[8:], 12)
		cd := bytes.Index(b, []byte{0x50, 0x4b, 0x01, 0x02})
		binary.LittleEndian.PutUint16(b[cd+10:], 12)
		_, err := ReadZip(b)
		if err == nil || !strings.Contains(err.Error(), "compression method") {
			t.Fatalf("err = %v, want unsupported compression method", err)
		}
	})

	t.Run("not a zip", func(t *testing.T) {
		if _, err := ReadZip([]byte("hello")); !IsUserError(err) {
			t.Fatalf("err = %v, want a user error", err)
		}
	})
}

// A zip whose headers understate the real size must still be cut off at the
// limit: the LimitReader, not the header, decides.
func TestReadZipForgedSize(t *testing.T) {
	big := strings.Repeat("a", MaxTotalBytes+10)
	b := makeZip(t, zipFile{name: "index.html", data: big, method: zip.Deflate})
	// Patch the uncompressed size in the central directory to 10 bytes.
	cd := bytes.Index(b, []byte{0x50, 0x4b, 0x01, 0x02})
	binary.LittleEndian.PutUint32(b[cd+24:], 10)
	_, err := ReadZip(b)
	if err == nil {
		t.Fatal("forged size was accepted")
	}
	if !IsUserError(err) {
		t.Errorf("err = %v, want a user error", err)
	}
}

func TestReadZipTooLarge(t *testing.T) {
	big := strings.Repeat("a", MaxTotalBytes/2+1)
	b := makeZip(t,
		zipFile{name: "index.html", data: big, method: zip.Deflate},
		zipFile{name: "b.html", data: big, method: zip.Deflate},
	)
	_, err := ReadZip(b)
	if !IsTooLarge(err) {
		t.Fatalf("err = %v, want too large", err)
	}
}

func TestReadZipRecordCount(t *testing.T) {
	var files []zipFile
	for i := 0; i <= maxRawRecords; i++ {
		files = append(files, zipFile{name: strings.Repeat("d", 1+i%3) + "/" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676)) + ".txt"})
	}
	_, err := ReadZip(makeZip(t, files...))
	if !IsTooLarge(err) {
		t.Fatalf("err = %v, want too large on record count", err)
	}

	// A truncated or corrupted EOCD offset is refused rather than trusted.
	b := makeZip(t, zipFile{name: "index.html", data: "x"})
	eocd := bytes.LastIndex(b, []byte{0x50, 0x4b, 0x05, 0x06})
	binary.LittleEndian.PutUint32(b[eocd+16:], 0xffffffff)
	if _, err := ReadZip(b); !IsUserError(err) {
		t.Fatalf("err = %v, want invalid zip", err)
	}
}

func TestNormalize(t *testing.T) {
	e := func(p string) Entry { return Entry{Path: p, Data: []byte("x")} }
	cases := []struct {
		name    string
		in      []Entry
		want    string // joined paths, or "" when an error is expected
		errLike string
	}{
		{"backslashes", []Entry{e(`index.html`), e(`css\a.css`)}, "css/a.css,index.html", ""},
		{"wrapper stripped", []Entry{e("w/index.html"), e("w/a/b.js")}, "a/b.js,index.html", ""},
		{"wrapper kept when root index exists", []Entry{e("index.html"), e("w/x.html")}, "index.html,w/x.html", ""},
		{"only one level stripped", []Entry{e("a/b/index.html")}, "", "index.html"},
		{"missing index", []Entry{e("a.html")}, "", "index.html"},
		{"case-sensitive index", []Entry{e("Index.html")}, "", "index.html"},
		{"absolute", []Entry{e("index.html"), e("/etc/x")}, "", "absolute"},
		{"dotdot", []Entry{e("index.html"), e("a/../b")}, "", "'..'"},
		{"dot", []Entry{e("index.html"), e("./b")}, "", "'..'"},
		{"empty segment", []Entry{e("index.html"), e("a//b")}, "", "empty path segment"},
		{"control char", []Entry{e("index.html"), e("a\nb")}, "", "control"},
		{"drive letter", []Entry{e("index.html"), e("C:/x")}, "", "drive"},
		{"duplicate", []Entry{e("index.html"), e("index.html")}, "", "more than once"},
		{"file and dir", []Entry{e("index.html"), e("a"), e("a/b")}, "", "both a file and a directory"},
		{"long path", []Entry{e("index.html"), e(strings.Repeat("p", MaxPathBytes+1))}, "", "at most"},
		{"junk only", []Entry{e(".DS_Store")}, "", "no files"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Normalize(c.in)
			if c.errLike != "" {
				if err == nil || !strings.Contains(err.Error(), c.errLike) {
					t.Fatalf("err = %v, want it to mention %q", err, c.errLike)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize: %v", err)
			}
			if paths(got) != c.want {
				t.Errorf("paths = %q, want %q", paths(got), c.want)
			}
		})
	}

	t.Run("too many entries", func(t *testing.T) {
		in := []Entry{e("index.html")}
		for i := range MaxEntries {
			in = append(in, e("f/"+strings.Repeat("x", i+1)))
		}
		if _, err := Normalize(in); !IsTooLarge(err) {
			t.Fatalf("err = %v, want too large", err)
		}
	})
	t.Run("too many bytes", func(t *testing.T) {
		in := []Entry{{Path: "index.html", Data: make([]byte, MaxTotalBytes+1)}}
		if _, err := Normalize(in); !IsTooLarge(err) {
			t.Fatalf("err = %v, want too large", err)
		}
	})
}

func TestPackRoundTripIsDeterministic(t *testing.T) {
	in := []Entry{
		{Path: "z.js", Data: []byte("console.log(1)")},
		{Path: "index.html", Data: []byte("<p>hi</p>")},
		{Path: "目录/页.html", Data: []byte("<p>页</p>")},
	}
	a, err := Pack(in)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	// Reversed input, same output bytes.
	b, err := Pack([]Entry{in[2], in[1], in[0]})
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Error("Pack is not deterministic")
	}

	back, err := ReadZip(a)
	if err != nil {
		t.Fatalf("ReadZip(Pack()): %v", err)
	}
	if want := "index.html,z.js,目录/页.html"; paths(back) != want {
		t.Errorf("paths = %q, want %q", paths(back), want)
	}

	arc, err := Open(a)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	data, ok, err := arc.Read("目录/页.html")
	if err != nil || !ok || string(data) != "<p>页</p>" {
		t.Errorf("Read = %q, %v, %v", data, ok, err)
	}
	if _, ok, _ := arc.Read("INDEX.HTML"); ok {
		t.Error("lookup should be case-sensitive")
	}
	list := arc.List()
	if len(list) != 3 || list[0].Path != "index.html" || list[0].Size != 9 {
		t.Errorf("List = %+v", list)
	}
}

func TestMerge(t *testing.T) {
	old := []Entry{
		{Path: "index.html", Data: []byte("old index")},
		{Path: "a.css", Data: []byte("a")},
		{Path: "logo.png", Data: []byte{0x89, 'P', 'N', 'G'}},
	}

	got, err := Merge(old, []Entry{{Path: "index.html", Data: []byte("new")}, {Path: "b/c.js", Data: []byte("c")}}, []string{"a.css"})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if want := "b/c.js,index.html,logo.png"; paths(got) != want {
		t.Errorf("paths = %q, want %q", paths(got), want)
	}
	for _, e := range got {
		if e.Path == "index.html" && string(e.Data) != "new" {
			t.Errorf("index.html = %q, want overwritten", e.Data)
		}
		if e.Path == "logo.png" && len(e.Data) != 4 {
			t.Errorf("untouched binary entry changed: %v", e.Data)
		}
	}

	if _, err := Merge(old, nil, []string{"index.html"}); err == nil || !strings.Contains(err.Error(), "index.html") {
		t.Errorf("deleting index.html: err = %v, want refusal", err)
	}
	if _, err := Merge(old, nil, []string{"nope.js"}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("deleting a missing path: err = %v", err)
	}
	if _, err := Merge(old, []Entry{{Path: "a.css"}}, []string{"a.css"}); err == nil {
		t.Error("put and delete of the same path should be refused")
	}
	if _, err := Merge(old, []Entry{{Path: "../x", Data: []byte("x")}}, nil); err == nil {
		t.Error("an unsafe put path should be refused")
	}

	// A patch that leaves everything in one directory must not trigger
	// wrapper stripping and silently restructure the site.
	nested := []Entry{{Path: "index.html", Data: []byte("i")}, {Path: "docs/a.html", Data: []byte("a")}}
	if _, err := Merge(nested, []Entry{{Path: "docs/index.html", Data: []byte("d")}}, []string{"index.html"}); err == nil {
		t.Error("merge should not strip a wrapper directory")
	}
}

func TestContentType(t *testing.T) {
	cases := []struct {
		path string
		data string
		want string
	}{
		{"index.html", "<p>hi</p>", "text/html; charset=utf-8"},
		{"a/B.HTM", "x", "text/html; charset=utf-8"},
		{"gbk.html", "\xc4\xe3\xba\xc3", "text/html"},
		{"s.css", "", "text/css; charset=utf-8"},
		{"m.mjs", "", "text/javascript; charset=utf-8"},
		{"d.json", "{}", "application/json; charset=utf-8"},
		{"i.svg", "<svg/>", "image/svg+xml; charset=utf-8"},
		{"p.png", "\x89PNG", "image/png"},
		{"f.woff2", "", "font/woff2"},
		{"README", "x", "application/octet-stream"},
		{"dir.v2/file", "x", "application/octet-stream"},
		{"x.exe", "x", "application/octet-stream"},
	}
	for _, c := range cases {
		if got := ContentType(c.path, []byte(c.data)); got != c.want {
			t.Errorf("ContentType(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestTitle(t *testing.T) {
	cases := map[string]string{
		"<html><head><TITLE>\n  My   &amp; Site </TITLE>": "My & Site",
		"<p>no title</p>":               "",
		`<title lang="en">中文标题</title>`: "中文标题",
	}
	for in, want := range cases {
		if got := Title([]Entry{{Path: "index.html", Data: []byte(in)}}); got != want {
			t.Errorf("Title(%q) = %q, want %q", in, got, want)
		}
	}
}
