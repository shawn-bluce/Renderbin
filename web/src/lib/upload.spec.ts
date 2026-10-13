import { describe, expect, it } from 'vitest';
import { detectKind, isZip, planUpload } from './upload';

function file(name: string, size = 10, type = ''): File {
	return { name, size, type } as File;
}

describe('detectKind', () => {
	it('goes by extension first, case-insensitively', () => {
		expect(detectKind(file('a.HTML'))).toBe('html');
		expect(detectKind(file('a.htm'))).toBe('html');
		expect(detectKind(file('a.md'))).toBe('markdown');
		expect(detectKind(file('a.markdown'))).toBe('markdown');
		expect(detectKind(file('a.txt'))).toBe('txt');
		expect(detectKind(file('a.text'))).toBe('txt');
	});

	it('falls back to the MIME type, and refuses everything else', () => {
		expect(detectKind(file('noext', 1, 'text/html'))).toBe('html');
		expect(detectKind(file('noext', 1, 'text/markdown'))).toBe('markdown');
		expect(detectKind(file('noext', 1, 'text/plain'))).toBe('txt');
		expect(detectKind(file('a.png', 1, 'image/png'))).toBeNull();
	});
});

describe('isZip', () => {
	it('matches the extension or the MIME type', () => {
		expect(isZip(file('site.ZIP'))).toBe(true);
		expect(isZip(file('blob', 1, 'application/zip'))).toBe(true);
		expect(isZip(file('a.html'))).toBe(false);
	});
});

describe('planUpload', () => {
	it('separates documents, zips, unsupported and oversize files', () => {
		const plan = planUpload([
			file('a.md'),
			file('b.html'),
			file('big.md', 5 * 1024 * 1024 + 1),
			file('site.zip'),
			file('photo.png', 1, 'image/png')
		]);
		expect(plan.documents.map((d) => [d.file.name, d.kind])).toEqual([
			['a.md', 'markdown'],
			['b.html', 'html']
		]);
		expect(plan.zips.map((z) => z.name)).toEqual(['site.zip']);
		expect(plan.skipped).toBe(1);
		expect(plan.oversize).toBe(1);
	});
});
