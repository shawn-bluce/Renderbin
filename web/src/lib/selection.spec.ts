import { describe, expect, it } from 'vitest';
import {
	MAX_QUOTE,
	clampQuote,
	explainAllowed,
	isPointerMessage,
	locateRequest,
	measureRequest,
	parseLocatedMessage,
	parseMeasuredMessage,
	parseScrollMessage,
	parseSelectionMessage
} from './selection';

const rect = { x: 10, y: 20, w: 30, h: 12 };

describe('parseSelectionMessage', () => {
	it('accepts a comment or explain message and collapses whitespace', () => {
		expect(
			parseSelectionMessage({ rb: 'selection', action: 'comment', text: ' a \n  b ', rect })
		).toEqual({ action: 'comment', text: 'a b', rect });
		expect(
			parseSelectionMessage({ rb: 'selection', action: 'explain', text: 'x', rect })?.action
		).toBe('explain');
	});

	it('refuses anything that is not exactly that shape', () => {
		for (const bad of [
			null,
			undefined,
			'selection',
			42,
			[],
			{},
			{ rb: 'locate', action: 'comment', text: 'x', rect },
			{ rb: 'selection', action: 'delete', text: 'x', rect },
			{ rb: 'selection', action: 'comment', rect },
			{ rb: 'selection', action: 'comment', text: 7, rect },
			{ rb: 'selection', action: 'comment', text: '   ', rect },
			{ rb: 'selection', action: 'comment', text: 'x' },
			{ rb: 'selection', action: 'comment', text: 'x', rect: { x: 1, y: 2, w: 3 } },
			{ rb: 'selection', action: 'comment', text: 'x', rect: { ...rect, y: NaN } },
			{ rb: 'selection', action: 'comment', text: 'x', rect: { ...rect, x: '5' } },
			{ rb: 'selection', action: 'comment', text: 'x', rect: { ...rect, w: 1e12 } }
		]) {
			expect(parseSelectionMessage(bad)).toBeNull();
		}
	});

	it('cuts a long selection by characters, not UTF-16 units', () => {
		const msg = parseSelectionMessage({
			rb: 'selection',
			action: 'comment',
			text: '\u{1F600}'.repeat(900),
			rect
		});
		expect(Array.from(msg!.text)).toHaveLength(MAX_QUOTE);
		expect(clampQuote('x'.repeat(MAX_QUOTE))).toHaveLength(MAX_QUOTE);
	});
});

describe('scroll, pointer and measured messages', () => {
	it('reads a scroll offset only when it is a sane number', () => {
		expect(parseScrollMessage({ rb: 'scroll', y: 120.5 })).toBe(120.5);
		expect(parseScrollMessage({ rb: 'scroll', y: -5 })).toBe(0);
		for (const bad of [
			null,
			{},
			{ rb: 'scroll' },
			{ rb: 'scroll', y: '9' },
			{ rb: 'scroll', y: Infinity }
		]) {
			expect(parseScrollMessage(bad)).toBeNull();
		}
	});

	it('recognizes a pointer message', () => {
		expect(isPointerMessage({ rb: 'pointer' })).toBe(true);
		expect(isPointerMessage({ rb: 'scroll' })).toBe(false);
		expect(isPointerMessage('pointer')).toBe(false);
	});

	it('keeps only well-formed measurements', () => {
		expect(
			parseMeasuredMessage({
				rb: 'measured',
				items: [
					{ id: 1, found: true, top: 300 },
					{ id: 2, found: false, top: 0 },
					{ id: 'x', found: true, top: 5 },
					{ id: 3, found: 'yes', top: 5 },
					{ id: 4, found: true, top: NaN },
					null
				]
			})
		).toEqual([
			{ id: 1, found: true, top: 300 },
			{ id: 2, found: false, top: 0 }
		]);
		expect(parseMeasuredMessage({ rb: 'measured' })).toBeNull();
		expect(parseMeasuredMessage({ rb: 'scroll', items: [] })).toBeNull();
	});

	it('builds a bounded measure request', () => {
		expect(measureRequest([{ id: 1, text: '  a   b ', ai: true }])).toEqual({
			rb: 'measure',
			items: [{ id: 1, text: 'a b', ai: true }]
		});
		expect(
			Array.from(measureRequest([{ id: 1, text: 'z'.repeat(2000), ai: false }]).items[0].text)
		).toHaveLength(MAX_QUOTE);
	});
});

describe('locate / located', () => {
	it('builds a bounded locate request', () => {
		expect(locateRequest('  a   b ')).toEqual({ rb: 'locate', text: 'a b' });
		expect(Array.from(locateRequest('y'.repeat(2000)).text)).toHaveLength(MAX_QUOTE);
	});

	it('reads only a real located answer', () => {
		expect(parseLocatedMessage({ rb: 'located', ok: true })).toEqual({ ok: true });
		expect(parseLocatedMessage({ rb: 'located', ok: 'yes' })).toEqual({ ok: false });
		expect(parseLocatedMessage({ rb: 'selection' })).toBeNull();
		expect(parseLocatedMessage('located')).toBeNull();
	});
});

describe('explainAllowed', () => {
	it('allows one explain per gap', () => {
		expect(explainAllowed(10_000, 0)).toBe(true);
		expect(explainAllowed(1_000, 0)).toBe(false);
		expect(explainAllowed(3_000, 0)).toBe(true);
		expect(explainAllowed(1_500, 1_000, 400)).toBe(true);
	});
});
