import { describe, expect, it } from 'vitest';
import { layoutNotes } from './notes-layout';

describe('layoutNotes', () => {
	it('puts a lone note level with its passage', () => {
		expect(layoutNotes([{ key: 'a', anchor: 120, height: 50 }]).get('a')).toBe(120);
	});

	it('pushes overlapping notes down in document order, with a gap', () => {
		const tops = layoutNotes(
			[
				{ key: 'b', anchor: 110, height: 40 },
				{ key: 'a', anchor: 100, height: 60 },
				{ key: 'c', anchor: 400, height: 30 }
			],
			8
		);
		expect(tops.get('a')).toBe(100); // first by position
		expect(tops.get('b')).toBe(168); // 100 + 60 + 8, not its own 110
		expect(tops.get('c')).toBe(400); // far enough away, stays level
	});

	it('is stable for equal anchors and for no notes', () => {
		expect(layoutNotes([]).size).toBe(0);
		const tops = layoutNotes(
			[
				{ key: 'x', anchor: 10, height: 20 },
				{ key: 'y', anchor: 10, height: 20 }
			],
			0
		);
		expect(tops.get('x')).toBe(10);
		expect(tops.get('y')).toBe(30);
	});
});
