// Markdown for "Ask AI" answers: marked to HTML, then DOMPurify, so nothing a
// model writes can run script in the app's origin.
//
// The allow-list is narrower than DOMPurify's default on purpose. The model
// reads a document someone else may have written, and that document can tell
// it what to output: an image whose URL carries the conversation would be
// fetched the moment the answer renders, leaking it without a click. So images
// are shown as links, and no tag that loads anything by itself is allowed.
import DOMPurify from 'dompurify';
import { Marked } from 'marked';

function escapeHTML(s: string): string {
	return s.replace(
		/[&<>"']/g,
		(c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!
	);
}

const marked = new Marked({
	gfm: true,
	breaks: true,
	renderer: {
		image({ href, text }) {
			return `<a href="${escapeHTML(href)}">${escapeHTML(text || href)}</a>`;
		}
	}
});

const ALLOWED_TAGS = [
	'p',
	'br',
	'hr',
	'h1',
	'h2',
	'h3',
	'h4',
	'h5',
	'h6',
	'strong',
	'em',
	'del',
	's',
	'code',
	'pre',
	'blockquote',
	'ul',
	'ol',
	'li',
	'a',
	'table',
	'thead',
	'tbody',
	'tr',
	'th',
	'td',
	'sup',
	'sub',
	'span'
];
const ALLOWED_ATTR = ['href', 'title', 'align', 'start', 'class'];

let hooked = false;

/** Renders model output as sanitized HTML; links open in a new tab. */
export function renderMarkdown(src: string): string {
	if (!hooked) {
		DOMPurify.addHook('afterSanitizeAttributes', (node) => {
			if (node.tagName === 'A') {
				node.setAttribute('target', '_blank');
				node.setAttribute('rel', 'noopener noreferrer');
			}
		});
		hooked = true;
	}
	const html = marked.parse(src, { async: false });
	return DOMPurify.sanitize(html, { ALLOWED_TAGS, ALLOWED_ATTR });
}
