package handlers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"rsc.io/qr"
)

// The "open on your phone" widget: every page served at /res carries a small
// QR button in its bottom-right corner (while ConfigQRWidget is on), and
// hovering it shows a QR code of the page's URL, drawn by GET /api/qr.
//
// The URL is the browser's location.href, not one the server assembles: behind
// a reverse proxy that does not forward the Host header, requestBaseURL would
// encode http://127.0.0.1:8080/... and every phone would get a dead link.

// maxQRText bounds GET /api/qr. A version-40 code at level M holds 2331 bytes,
// and no URL the widget sends comes close.
const maxQRText = 2048

// qrQuietZone is the blank margin, in modules, the QR spec asks for so a
// scanner can find the code's edges.
const qrQuietZone = 4

// QRCode serves GET /api/qr?text=... as an SVG QR code of text. It is public:
// the widget asks for it from a sandboxed page, whose requests carry no cookie,
// and the image holds nothing the caller did not send.
func QRCode(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if text == "" || len(text) > maxQRText {
		http.Error(w, fmt.Sprintf("text must be 1 to %d bytes", maxQRText), http.StatusBadRequest)
		return
	}
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		http.Error(w, "text does not fit in a QR code", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	// The same text always draws the same code. private: the text is a share
	// link, access code included, which has no business in a shared cache.
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(qrSVG(code.Size, code.Black))
}

// qrSVG draws a size x size module grid as one SVG path, one subpath per
// horizontal run of dark modules, inside the quiet zone.
func qrSVG(size int, dark func(x, y int) bool) []byte {
	var b bytes.Buffer
	n := size + 2*qrQuietZone
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, n, n)
	b.WriteString(`<rect width="100%" height="100%" fill="#fff"/><path fill="#000" d="`)
	for y := range size {
		for x := 0; x < size; {
			if !dark(x, y) {
				x++
				continue
			}
			start := x
			for x < size && dark(x, y) {
				x++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", start+qrQuietZone, y+qrQuietZone, x-start, x-start)
		}
	}
	b.WriteString(`"/></svg>`)
	return b.Bytes()
}

// What the widget says under the code when the link will not simply open on
// another device.
const (
	qrNoteNone    = ""
	qrNotePrivate = "private" // a private file: the phone has to sign in
	qrNotePreview = "preview" // a site's owner preview link, valid for 6 hours
)

// withQRWidget adds the widget to a page about to be served, unless the super
// admin has turned it off.
func (h *FilesHandler) withQRWidget(ctx context.Context, page []byte, note string) []byte {
	if !configBool(ctx, h.queries, ConfigQRWidget) {
		return page
	}
	return injectQRWidget(page, note)
}

// injectQRWidget inserts the widget's script before the page's last </body>,
// or appends it when there is none (the parser puts trailing content in the
// body anyway). It returns a new slice; page is not modified.
func injectQRWidget(page []byte, note string) []byte {
	script := strings.Replace(qrWidgetScript, "__NOTE__", note, 1)
	i := lastBodyClose(page)
	if i < 0 {
		i = len(page)
	}
	out := make([]byte, 0, len(page)+len(script))
	out = append(out, page[:i]...)
	out = append(out, script...)
	return append(out, page[i:]...)
}

// lastBodyClose finds the last "</body" in any letter case. It compares ASCII
// only rather than lower-casing the page, which would shift offsets wherever
// the bytes are not UTF-8 -- a GBK document is entirely legal here.
func lastBodyClose(page []byte) int {
	for i := bytes.LastIndex(page, []byte("</")); i >= 0; i = bytes.LastIndex(page[:i], []byte("</")) {
		if len(page)-i >= 6 && strings.EqualFold(string(page[i+2:i+6]), "body") {
			return i
		}
	}
	return -1
}

// qrWidgetScript builds the widget from script so that a page that blocks
// inline scripts gets no widget at all rather than a dead button. It lives in
// a closed shadow root under a custom element, so the page's CSS cannot restyle
// it and its CSS cannot leak into the page. It stays out of the way where it
// would be useless: inside a frame (an embedded document is not the page the
// visitor is on), on devices that cannot hover (a phone is where the link is
// going), and in print. The code itself is fetched on first hover, so a visit
// that never hovers costs no request.
//
// Pure ASCII on purpose, the Chinese text included (as \u escapes): it is
// spliced into uploaded documents, and one declaring a legacy charset such as
// GBK would show raw UTF-8 bytes as mojibake.
//
// __NOTE__ is replaced with one of the qrNote* constants.
const qrWidgetScript = `<script>(function(note){try{
if(window.top!==window.self||!window.matchMedia||!matchMedia('(any-hover: hover)').matches)return;
var me=document.currentScript;if(me)me.remove();
var zh=/^zh\b/i.test(navigator.language||'');
var caption={
'':zh?'\u624b\u673a\u626b\u7801\u67e5\u770b':'Scan to open on your phone',
'private':zh?'\u79c1\u6709\u6587\u4ef6\uff1a\u624b\u673a\u4e0a\u9700\u767b\u5f55\u540e\u624d\u80fd\u67e5\u770b':'Private file: sign in on your phone to view it',
'preview':zh?'\u9884\u89c8\u94fe\u63a5\uff0c6 \u5c0f\u65f6\u5185\u6709\u6548':'Preview link, valid for 6 hours'
}[note]||'';
var host=document.createElement('renderbin-qr');
host.style.cssText='all:initial!important;display:block!important;position:fixed!important;right:16px!important;bottom:16px!important;z-index:2147483647!important';
var root=host.attachShadow({mode:'closed'});
root.innerHTML='<style>'+
'button{all:unset;box-sizing:border-box;display:flex;align-items:center;justify-content:center;width:36px;height:36px;border-radius:50%;background:#fff;color:#3c3836;border:1px solid rgba(0,0,0,.15);box-shadow:0 1px 4px rgba(0,0,0,.2);cursor:pointer;opacity:.6;transition:opacity .15s}'+
':host(:hover) button,button:focus-visible{opacity:1}'+
'button:focus-visible{outline:2px solid #458588;outline-offset:2px}'+
'.panel{display:none;position:absolute;right:0;bottom:100%;padding-bottom:8px}'+
':host(:hover) .panel,:host(:focus-within) .panel{display:block}'+
'.card{box-sizing:border-box;width:184px;padding:10px;background:#fff;color:#3c3836;border:1px solid rgba(0,0,0,.12);border-radius:10px;box-shadow:0 4px 16px rgba(0,0,0,.18);font:12px/1.4 -apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;text-align:center}'+
'img{display:block;width:162px;height:162px;margin:0 auto}'+
'p{margin:6px 0 0}'+
'@media print{button,.panel{display:none!important}}'+
'</style>'+
'<div class="panel" role="tooltip" id="qr"><div class="card"><img alt=""><p></p></div></div>'+
'<button type="button" aria-describedby="qr"><svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" aria-hidden="true"><g fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="2"><rect width="5" height="5" x="3" y="3" rx="1"/><rect width="5" height="5" x="16" y="3" rx="1"/><rect width="5" height="5" x="3" y="16" rx="1"/><path d="M21 16h-3a2 2 0 0 0-2 2v3m5 0v.01M12 7v3a2 2 0 0 1-2 2H7m-4 0h.01M12 3h.01M12 16v.01M16 12h1m4 0v.01M12 21v-1"/></g></svg></button>';
var img=root.querySelector('img');
root.querySelector('p').textContent=caption;
img.alt=caption;
root.querySelector('button').setAttribute('aria-label',zh?'\u5728\u624b\u673a\u4e0a\u6253\u5f00':'Open on your phone');
// Resolved against location.href, not the document: a <base href> in the
// page would otherwise send the request to another host.
function load(){var u=new URL('/api/qr',location.href);u.searchParams.set('text',location.href);if(img.src!==u.href)img.src=u.href}
host.addEventListener('mouseenter',load);
host.addEventListener('focusin',load);
(document.body||document.documentElement).appendChild(host);
}catch(e){}})("__NOTE__")</script>`
