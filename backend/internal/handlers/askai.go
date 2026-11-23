package handlers

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

// The "Ask AI" button on /res pages. It is only a link: the conversation
// happens on the app's /view/{slug} page, which frames the same document and
// puts the chat beside it.
//
// The chat cannot live in the document itself. A /res page runs in the
// sandbox's opaque origin, whose requests carry no cookie, and the document's
// own scripts can read and rewrite anything on it -- so a chat there would
// need a credential the document's author could use, and would show the
// reader's conversations to that author's code. The link carries nothing
// secret: the target is the page's own slug and the access code already in its
// URL.
//
// It is offered when a model is configured and the server knows the reader is
// signed in, since /view requires an account:
//   - a document (Render reads the session cookie): to its owner, and to a
//     signed-in reader holding the code -- unless the file has a view limit,
//     because /view loads the document again, which would spend another view
//     and could take a burn-after-reading link offline under the reader;
//   - a site page (RenderSite reads no cookie by design): to the owner, on
//     their preview link, which only the owner is ever given.

// askAITarget is the /view URL for a file, with the access code a non-owner
// needs.
func askAITarget(slug, code string) string {
	target := "/view/" + url.PathEscape(slug)
	if code != "" {
		target += "?code=" + url.QueryEscape(code)
	}
	return target
}

// withAskAI adds the button to a page about to be served, when a model is
// configured.
func (h *FilesHandler) withAskAI(ctx context.Context, page []byte, target string) []byte {
	if !aiEnabled(ctx, h.queries) {
		return page
	}
	return injectAskAIWidget(page, target)
}

func injectAskAIWidget(page []byte, target string) []byte {
	// json.Marshal escapes <, > and & and every non-ASCII rune, so the
	// literal can neither close the script nor turn into mojibake.
	lit, _ := json.Marshal(target)
	return injectScript(page, strings.Replace(askAIWidgetScript, "__TARGET__", string(lit), 1))
}

// askAIWidgetScript follows qrWidgetScript's rules: built from script, in a
// closed shadow root, absent inside a frame (the /view page frames this very
// document and has its own button) and in print, and pure ASCII. It sits just
// above the QR button. The link is resolved against location.href rather than
// the document, whose <base href> could point it at another host.
const askAIWidgetScript = `<script>(function(target){try{
if(window.top!==window.self)return;
var me=document.currentScript;if(me)me.remove();
var zh=/^zh\b/i.test(navigator.language||'');
var host=document.createElement('renderbin-ask-ai');
host.style.cssText='all:initial!important;display:block!important;position:fixed!important;right:16px!important;bottom:60px!important;z-index:2147483647!important';
var root=host.attachShadow({mode:'closed'});
root.innerHTML='<style>'+
'a{all:unset;box-sizing:border-box;display:flex;align-items:center;gap:6px;height:36px;padding:0 14px 0 12px;border-radius:18px;background:#fff;color:#3c3836;border:1px solid rgba(0,0,0,.15);box-shadow:0 1px 4px rgba(0,0,0,.2);cursor:pointer;opacity:.85;transition:opacity .15s;font:600 13px/1 -apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif}'+
'a:hover,a:focus-visible{opacity:1}'+
'a:focus-visible{outline:2px solid #458588;outline-offset:2px}'+
'svg{color:#d65d0e}'+
'@media print{a{display:none!important}}'+
'</style>'+
'<a><svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" aria-hidden="true"><g fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="2"><path d="M11.017 2.814a1 1 0 0 1 1.966 0l1.051 5.558a2 2 0 0 0 1.594 1.594l5.558 1.051a1 1 0 0 1 0 1.966l-5.558 1.051a2 2 0 0 0-1.594 1.594l-1.051 5.558a1 1 0 0 1-1.966 0l-1.051-5.558a2 2 0 0 0-1.594-1.594l-5.558-1.051a1 1 0 0 1 0-1.966l5.558-1.051a2 2 0 0 0 1.594-1.594zM20 2v4m2-2h-4"/><circle cx="4" cy="20" r="2"/></g></svg><span></span></a>';
var a=root.querySelector('a');
a.href=new URL(target,location.href).href;
root.querySelector('span').textContent=zh?'\u95ee AI':'Ask AI';
a.title=zh?'\u548c AI \u8ba8\u8bba\u8fd9\u7bc7\u6587\u6863':'Chat with AI about this document';
(document.body||document.documentElement).appendChild(host);
}catch(e){}})(__TARGET__)</script>`
