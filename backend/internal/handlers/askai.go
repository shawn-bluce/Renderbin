package handlers

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// What /res pages offer a signed-in reader: a link to the app's /view/{slug}
// page and, once the document is framed there, a toolbar on selected text.
// /view frames the same document and puts the panels beside it -- comments, and
// the "Ask AI" chat when a model is configured.
//
// Neither can live in the document itself. A /res page runs in the sandbox's
// opaque origin, whose requests carry no cookie, and the document's own scripts
// can read and rewrite anything on it -- so a chat or a comment form there
// would need a credential the document's author could use, and would show the
// reader's activity to that author's code. The link carries nothing secret: the
// target is the page's own slug and the access code already in its URL. The
// toolbar hands the selected text to /view with postMessage and nothing else;
// /view treats the message as untrusted input (web/src/lib/selection.ts).
//
// They are offered when the server knows the reader is signed in, since /view
// requires an account:
//   - a document (Render reads the session cookie): to its owner, and to a
//     signed-in reader holding the code -- unless the file has a view limit,
//     because /view loads the document again, which would spend another view
//     and could take a burn-after-reading link offline under the reader;
//   - a site page (RenderSite reads no cookie by design): to the owner, on
//     their preview link, which only the owner is ever given.
//
// Comments do not need a model, so the link is always offered; with a model
// configured it reads "Ask AI", without one "Comments", and the toolbar's
// "Explain with AI" button appears only when a model is configured.

// askAITarget is the /view URL for a file, with the access code a non-owner
// needs.
func askAITarget(slug, code string) string {
	target := "/view/" + url.PathEscape(slug)
	if code != "" {
		target += "?code=" + url.QueryEscape(code)
	}
	return target
}

// withViewWidgets adds the /view link and the selection toolbar to a page
// about to be served.
func (h *FilesHandler) withViewWidgets(ctx context.Context, page []byte, target string) []byte {
	ai := aiEnabled(ctx, h.queries)
	return injectSelectionWidget(injectAskAIWidget(page, target, ai), ai)
}

func injectAskAIWidget(page []byte, target string, ai bool) []byte {
	// json.Marshal escapes <, > and & and every non-ASCII rune, so the
	// literal can neither close the script nor turn into mojibake.
	lit, _ := json.Marshal(target)
	script := strings.Replace(askAIWidgetScript, "__TARGET__", string(lit), 1)
	script = strings.Replace(script, "__AI__", strconv.FormatBool(ai), 1)
	return injectScript(page, script)
}

func injectSelectionWidget(page []byte, explain bool) []byte {
	return injectScript(page, strings.Replace(selectionWidgetScript, "__EXPLAIN__", strconv.FormatBool(explain), 1))
}

// askAIWidgetScript follows qrWidgetScript's rules: built from script, in a
// closed shadow root, absent inside a frame (the /view page frames this very
// document and has its own button) and in print, and pure ASCII. It sits just
// above the QR button. The link is resolved against location.href rather than
// the document, whose <base href> could point it at another host.
const askAIWidgetScript = `<script>(function(target,ai){try{
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
root.querySelector('span').textContent=ai?(zh?'\u95ee AI':'Ask AI'):(zh?'\u8bc4\u8bba':'Comments');
a.title=ai?(zh?'\u548c AI \u8ba8\u8bba\u8fd9\u7bc7\u6587\u6863':'Chat with AI about this document'):(zh?'\u8bc4\u8bba\u8fd9\u7bc7\u6587\u6863':'Comment on this document');
(document.body||document.documentElement).appendChild(host);
}catch(e){}})(__TARGET__,__AI__)</script>`

// selectionWidgetScript is the toolbar over selected text, "Comment" and
// (with a model) "Explain with AI", plus the document's half of the margin notes
// /view draws beside it. It works only inside /view's frame -- the opposite of
// the two buttons above, which hide there -- and only in a frame whose parent is
// the top window (a document's own nested iframe is not /view's). It follows the
// same rules: built from script, in a closed shadow root, absent in print, pure
// ASCII.
//
// It speaks to /view with postMessage. Out:
//
//	{rb:'selection', action:'comment'|'explain', text, rect}  a toolbar button was
//	    clicked; text is cut to 500 characters, rect is the selection's box in
//	    the frame's viewport (which is /view's viewport too: the frame starts at
//	    its top left)
//	{rb:'pointer'}   the document was clicked (lets /view close an empty input)
//	{rb:'scroll', y} the document scrolled
//	{rb:'measured', items:[{id, found, top}]}  where each requested passage is, as
//	    a distance from the top of the document
//	{rb:'located', ok}  answer to locate
//
// In: {rb:'measure', items:[{id, text, ai}]} (find those passages now, and again
// whenever the layout changes; every one found is also highlighted, AI
// explanations in another colour, with the CSS Custom Highlight API, which marks
// text without touching the document's DOM -- a browser without it simply shows
// no highlight) and {rb:'locate', text} (scroll to the passage and
// select it; the selection highlight is the marker). Passages are found by
// searching the document's text with whitespace collapsed, the way a selection's
// text arrives, so a passage that no longer exists is "not found", never wrong.
// The target origin is '*' because a sandboxed page's own origin is opaque and
// /view's cannot be named from it; the payloads are passages of the very
// document the framer already loaded.
const selectionWidgetScript = `<script>(function(explain){try{
if(window.top===window.self||window.parent!==window.top)return;
var me=document.currentScript;if(me)me.remove();
var zh=/^zh\b/i.test(navigator.language||'');
var host=document.createElement('renderbin-selection');
host.style.cssText='all:initial!important;display:none!important;position:fixed!important;left:0!important;top:0!important;z-index:2147483647!important';
var root=host.attachShadow({mode:'closed'});
root.innerHTML='<style>'+
'.bar{display:flex;gap:2px;padding:4px;border-radius:8px;background:#3c3836;box-shadow:0 2px 8px rgba(0,0,0,.35);font:600 13px/1 -apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif}'+
'button{all:unset;box-sizing:border-box;padding:7px 11px;border-radius:5px;color:#fbf1c7;cursor:pointer;white-space:nowrap}'+
'button:hover,button:focus-visible{background:rgba(255,255,255,.16)}'+
'button:focus-visible{outline:2px solid #83a598}'+
'@media print{.bar{display:none!important}}'+
'</style><div class="bar"><button type="button" data-a="comment"></button>'+(explain?'<button type="button" data-a="explain"></button>':'')+'</div>';
root.querySelector('[data-a=comment]').textContent=zh?'\u8bc4\u8bba':'Comment';
var eb=root.querySelector('[data-a=explain]');
if(eb)eb.textContent=zh?'\u8ba9 AI \u89e3\u91ca':'Explain with AI';
var bar=root.querySelector('.bar');
var cur='',curRect=null,down=false,quiet=0,t=0,items=[],mt=0,st=0,hl=null;
function post(m){window.parent.postMessage(m,'*')}
function scrollY(){return window.pageYOffset||document.documentElement.scrollTop||0}
function hide(){host.style.setProperty('display','none','important')}
function show(){
if(Date.now()<quiet)return;
var s=window.getSelection();
if(!s||s.isCollapsed||!s.rangeCount)return hide();
var text=s.toString().replace(/\s+/g,' ').trim();
if(!text)return hide();
var r=s.getRangeAt(0).getBoundingClientRect();
if(!r.width&&!r.height)return hide();
cur=Array.from(text).slice(0,500).join('');
curRect={x:r.left,y:r.top,w:r.width,h:r.height};
host.style.setProperty('display','block','important');
var w=bar.offsetWidth,h=bar.offsetHeight;
var x=Math.max(8,Math.min(r.left+r.width/2-w/2,window.innerWidth-w-8));
var y=r.bottom+8;if(y+h>window.innerHeight-8)y=Math.max(8,r.top-h-8);
host.style.setProperty('left',x+'px','important');
host.style.setProperty('top',y+'px','important');
}
function later(){clearTimeout(t);t=setTimeout(show,200)}
document.addEventListener('mousedown',function(e){if(e.target===host)return;down=true;hide();post({rb:'pointer'})},true);
document.addEventListener('mouseup',function(e){if(e.target===host)return;down=false;later()},true);
document.addEventListener('selectionchange',function(){if(!down)later()});
document.addEventListener('scroll',hide,true);
window.addEventListener('scroll',function(){
if(st)return;
st=requestAnimationFrame(function(){st=0;post({rb:'scroll',y:scrollY()})});
},{passive:true});
root.addEventListener('mousedown',function(e){e.preventDefault()});
root.addEventListener('click',function(e){
var b=e.target&&e.target.closest?e.target.closest('button'):null;
if(!b||!cur||!curRect)return;
post({rb:'selection',action:b.getAttribute('data-a'),text:cur,rect:curRect});
hide();
});
var blocks=new Map();
function isBlock(el){
var v=blocks.get(el);
if(v===undefined){var d=getComputedStyle(el).display;v=!(d==='inline'||d==='inline-block'||d==='inline-flex'||d==='inline-grid'||d==='contents'||d==='ruby');blocks.set(el,v)}
return v;
}
function blockOf(n){var el=n.parentElement;while(el&&!isBlock(el))el=el.parentElement;return el}
function index(){
blocks=new Map();
var chars=[],map=[],space=true,last=null,n;
var tw=document.createTreeWalker(document.body||document.documentElement,NodeFilter.SHOW_TEXT,null);
while((n=tw.nextNode())){
var p=n.parentNode;
if(!p)continue;
var tag=p.nodeName;
if(tag==='SCRIPT'||tag==='STYLE'||tag==='NOSCRIPT'||tag==='TEMPLATE'||p===host)continue;
var b=blockOf(n);
if(last&&b!==last&&!space){chars.push(' ');map.push(null);space=true}
last=b;
var s=n.nodeValue;
for(var i=0;i<s.length;i++){
var c=s.charAt(i);
if(/\s/.test(c)){if(!space){chars.push(' ');map.push([n,i]);space=true}}
else{chars.push(c);map.push([n,i]);space=false}
}
}
return {text:chars.join(''),map:map};
}
function find(ix,q){
var qs=[q];
if(q.length>80)qs.push(q.slice(0,80));
for(var j=0;j<qs.length;j++){
var from=0,k;
while((k=ix.text.indexOf(qs[j],from))>=0){
var a=ix.map[k],z=ix.map[k+qs[j].length-1];
if(a&&z){
var r=document.createRange();
r.setStart(a[0],a[1]);r.setEnd(z[0],z[1]+1);
var rc=r.getClientRects();
if(rc.length&&(rc[0].width||rc[0].height))return {range:r,top:rc[0].top};
}
from=k+1;
}
}
return null;
}
function paint(plain,ai){
if(!(window.CSS&&CSS.highlights&&window.Highlight))return;
try{
if(!hl){
hl=document.createElement('style');
hl.textContent='::highlight(rb-note){background-color:rgba(250,189,47,.4)}::highlight(rb-note-ai){background-color:rgba(131,165,152,.45)}';
(document.head||document.documentElement).appendChild(hl);
}
CSS.highlights.delete('rb-note');CSS.highlights.delete('rb-note-ai');
var g=[['rb-note',plain],['rb-note-ai',ai]];
for(var i=0;i<g.length;i++){
if(!g[i][1].length)continue;
var h=new Highlight();
for(var j=0;j<g[i][1].length;j++)h.add(g[i][1][j]);
CSS.highlights.set(g[i][0],h);
}
}catch(e){}
}
function report(){
if(!items.length){paint([],[]);return}
var ix=index(),out=[],plain=[],ai=[];
for(var i=0;i<items.length;i++){
var f=find(ix,items[i].text);
if(f)(items[i].ai?ai:plain).push(f.range);
out.push(f?{id:items[i].id,found:true,top:Math.round(f.top+scrollY())}:{id:items[i].id,found:false,top:0});
}
paint(plain,ai);
post({rb:'measured',items:out});
post({rb:'scroll',y:scrollY()});
}
function soon(){clearTimeout(mt);mt=setTimeout(report,300)}
if(window.ResizeObserver){try{new ResizeObserver(soon).observe(document.documentElement)}catch(e){}}
window.addEventListener('load',soon);
window.addEventListener('message',function(e){
if(e.source!==window.parent)return;
var d=e.data;
if(!d)return;
if(d.rb==='measure'&&Array.isArray(d.items)){
var next=[];
for(var i=0;i<d.items.length&&i<500;i++){
var it=d.items[i];
if(it&&typeof it.id==='number'&&typeof it.text==='string'&&it.text)next.push({id:it.id,text:it.text.slice(0,600),ai:it.ai===true});
}
items=next;
clearTimeout(mt);mt=setTimeout(report,0);
return;
}
if(d.rb!=='locate'||typeof d.text!=='string')return;
quiet=Date.now()+1200;hide();
var f=find(index(),d.text);
var s=window.getSelection();
if(s){s.removeAllRanges();if(f)s.addRange(f.range)}
if(f){var el=f.range.startContainer.parentElement;if(el&&el.scrollIntoView)el.scrollIntoView({block:'center',behavior:'smooth'})}
post({rb:'located',ok:!!f});
});
(document.body||document.documentElement).appendChild(host);
}catch(e){}})(__EXPLAIN__)</script>`
