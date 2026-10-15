package handlers

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"rsc.io/qr"
)

func TestInjectQRWidget(t *testing.T) {
	cases := []struct {
		name, page, before, after string
	}{
		{"before body close", "<html><body><p>x</p></body></html>", "<html><body><p>x</p>", "</body></html>"},
		{"any letter case", "<p>x</p></BoDy >\n", "<p>x</p>", "</BoDy >\n"},
		// A "</body>" inside a script string earlier on is not the one.
		{"last one wins", "<script>s='</body>'</script><p>x</p></body>", "<script>s='</body>'</script><p>x</p>", "</body>"},
		{"no body: appended", "<h1>bare</h1>", "<h1>bare</h1>", ""},
		{"empty", "", "", ""},
		// Not UTF-8, and nothing may be re-encoded or shifted.
		{"gbk bytes kept", "<meta charset=gbk>\xc4\xe3\xba\xc3</body>", "<meta charset=gbk>\xc4\xe3\xba\xc3", "</body>"},
		{"truncated tag", "<p>x</p></bo", "<p>x</p></bo", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			page := []byte(c.page)
			got := string(injectQRWidget(page, qrNoteNone))
			if string(page) != c.page {
				t.Fatalf("input modified: %q", page)
			}
			before, rest, ok := strings.Cut(got, qrWidgetScript[:20])
			if !ok {
				t.Fatalf("no widget injected: %q", got)
			}
			_, after, ok := strings.Cut(rest, `")</script>`)
			if !ok || before != c.before || after != c.after {
				t.Errorf("injected at the wrong place:\n got %q + script + %q\nwant %q + script + %q", before, after, c.before, c.after)
			}
		})
	}

	for _, note := range []string{qrNoteNone, qrNotePrivate, qrNotePreview} {
		got := string(injectQRWidget(nil, note))
		if !strings.HasSuffix(got, `})("`+note+`")</script>`) {
			t.Errorf("note %q not passed to the script: ...%q", note, got[len(got)-40:])
		}
	}
}

// The widget is spliced into documents that may declare any charset, so it
// must not carry a byte whose meaning depends on one.
func TestQRWidgetScriptIsASCII(t *testing.T) {
	for i, c := range []byte(qrWidgetScript) {
		if c >= 0x80 {
			t.Fatalf("non-ASCII byte %#x at offset %d", c, i)
		}
	}
	if strings.Count(qrWidgetScript, "</script") != 1 {
		t.Fatal("the script must contain exactly one </script>, its own end")
	}
}

// Reading the SVG path back must give exactly the encoder's module grid.
func TestQRSVGMatchesCode(t *testing.T) {
	code, err := qr.Encode("https://example.com/res/abcd1234?code=Xy9_k-2Q", qr.M)
	if err != nil {
		t.Fatal(err)
	}
	svg := string(qrSVG(code.Size, code.Black))

	n := code.Size + 2*qrQuietZone
	if want := `viewBox="0 0 ` + strconv.Itoa(n) + " " + strconv.Itoa(n) + `"`; !strings.Contains(svg, want) {
		t.Fatalf("svg lacks %s: %.120s", want, svg)
	}

	drawn := make(map[[2]int]bool)
	run := regexp.MustCompile(`M(\d+) (\d+)h(\d+)v1h-(\d+)z`)
	for _, m := range run.FindAllStringSubmatch(svg, -1) {
		x, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[2])
		w, _ := strconv.Atoi(m[3])
		if m[3] != m[4] {
			t.Fatalf("run %q does not close", m[0])
		}
		for i := range w {
			drawn[[2]int{x + i - qrQuietZone, y - qrQuietZone}] = true
		}
	}
	for y := -qrQuietZone; y < code.Size+qrQuietZone; y++ {
		for x := -qrQuietZone; x < code.Size+qrQuietZone; x++ {
			if drawn[[2]int{x, y}] != code.Black(x, y) {
				t.Fatalf("module (%d,%d): drawn %v, code %v", x, y, drawn[[2]int{x, y}], code.Black(x, y))
			}
		}
	}
}
