// The bridge between a framed /res document and the /view page around it.
//
// The document runs sandboxed in an opaque origin, so the only channel is
// postMessage, and what arrives is *untrusted*: the document's own scripts can
// post anything. The injected toolbar (askai.go, selectionWidgetScript) posts
// {rb:'selection', action, text} when a button over the selected text is
// clicked, and answers {rb:'located', ok} to the {rb:'locate', text} this page
// sends. Everything here treats a message as data to validate, never as
// something to act on as given: the caller must also check that the message's
// `source` is the frame's window, and what it takes from a message is a
// whitespace-collapsed string of bounded length, rendered as text.

/** The longest passage a comment quotes; the server's limit is 4x this in bytes. */
export const MAX_QUOTE = 500;

export type SelectionAction = 'comment' | 'explain';

/** A box in the frame's viewport, which is this page's viewport too (the frame starts at its top left). */
export interface Rect {
	x: number;
	y: number;
	w: number;
	h: number;
}

export interface SelectionMessage {
	action: SelectionAction;
	text: string;
	rect: Rect;
}

/** Collapses whitespace and cuts to MAX_QUOTE characters (code points, not UTF-16 units). */
export function clampQuote(text: string): string {
	const t = text.replace(/\s+/g, ' ').trim();
	const chars = Array.from(t);
	return chars.length > MAX_QUOTE ? chars.slice(0, MAX_QUOTE).join('') : t;
}

function record(data: unknown): Record<string, unknown> | null {
	return data !== null && typeof data === 'object' ? (data as Record<string, unknown>) : null;
}

const COORD_LIMIT = 1e7;

/** A finite number within a sane range, else null. */
function coord(v: unknown): number | null {
	return typeof v === 'number' && Number.isFinite(v) && Math.abs(v) <= COORD_LIMIT ? v : null;
}

function parseRect(v: unknown): Rect | null {
	const r = record(v);
	if (!r) return null;
	const x = coord(r.x);
	const y = coord(r.y);
	const w = coord(r.w);
	const h = coord(r.h);
	return x === null || y === null || w === null || h === null ? null : { x, y, w, h };
}

/** A selection message from the frame, validated; null for anything else. */
export function parseSelectionMessage(data: unknown): SelectionMessage | null {
	const d = record(data);
	if (!d || d.rb !== 'selection') return null;
	if (d.action !== 'comment' && d.action !== 'explain') return null;
	if (typeof d.text !== 'string') return null;
	const rect = parseRect(d.rect);
	if (!rect) return null;
	const text = clampQuote(d.text);
	return text ? { action: d.action, text, rect } : null;
}

/** The frame's answer to a locate request; null for anything else. */
export function parseLocatedMessage(data: unknown): { ok: boolean } | null {
	const d = record(data);
	if (!d || d.rb !== 'located') return null;
	return { ok: d.ok === true };
}

/** The document was scrolled to `y` pixels; null for anything else. */
export function parseScrollMessage(data: unknown): number | null {
	const d = record(data);
	if (!d || d.rb !== 'scroll') return null;
	const y = coord(d.y);
	return y === null ? null : Math.max(0, y);
}

/** The document was clicked. */
export function isPointerMessage(data: unknown): boolean {
	return record(data)?.rb === 'pointer';
}

/** Where a requested passage is: `top` pixels below the top of the document, if found. */
export interface Measured {
	id: number;
	found: boolean;
	top: number;
}

/** The frame's answer to a measure request; null for anything else. */
export function parseMeasuredMessage(data: unknown): Measured[] | null {
	const d = record(data);
	if (!d || d.rb !== 'measured' || !Array.isArray(d.items)) return null;
	const out: Measured[] = [];
	for (const raw of d.items.slice(0, 500)) {
		const it = record(raw);
		const top = it ? coord(it.top) : null;
		if (!it || !Number.isInteger(it.id) || typeof it.found !== 'boolean' || top === null) continue;
		out.push({ id: it.id as number, found: it.found, top });
	}
	return out;
}

export interface MeasureItem {
	id: number;
	text: string;
	/** An AI explanation: highlighted in its own colour. */
	ai: boolean;
}

/**
 * What this page asks the frame to look for, and keep looking for as the layout
 * changes. The frame also highlights every passage it finds.
 */
export function measureRequest(items: MeasureItem[]): { rb: 'measure'; items: MeasureItem[] } {
	return {
		rb: 'measure',
		items: items.map((i) => ({ id: i.id, text: clampQuote(i.text), ai: i.ai }))
	};
}

/** What this page asks the frame to find and select. */
export function locateRequest(text: string): { rb: 'locate'; text: string } {
	return { rb: 'locate', text: clampQuote(text) };
}

/**
 * Whether an "explain" arriving at `now` may start. "Explain" sends a question
 * to the model as soon as it arrives, and the message could as well come from
 * the document's own script as from a click on the toolbar, so a burst is
 * refused: at most one every `minGapMs`.
 */
export function explainAllowed(now: number, lastAt: number, minGapMs = 3000): boolean {
	return now - lastAt >= minGapMs;
}
