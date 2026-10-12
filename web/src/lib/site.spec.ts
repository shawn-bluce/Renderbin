import { describe, expect, it } from 'vitest';
import { isJunkPath, planSite, stripWrapper, SITE_MAX_ENTRIES, type PickedFile } from './site';

function picked(path: string, size = 1): PickedFile {
	return { path, file: new File([new Uint8Array(size)], path.split('/').pop() ?? path) };
}

describe('isJunkPath', () => {
	it('recognizes operating-system litter', () => {
		expect(isJunkPath('__MACOSX/site/._index.html')).toBe(true);
		expect(isJunkPath('site/.DS_Store')).toBe(true);
		expect(isJunkPath('site/._a.css')).toBe(true);
		expect(isJunkPath('Thumbs.db')).toBe(true);
		expect(isJunkPath('site/index.html')).toBe(false);
		expect(isJunkPath('site/.well-known/x')).toBe(false);
	});
});

describe('stripWrapper', () => {
	it('drops the folder a directory picker puts in front of every path', () => {
		const out = stripWrapper([picked('my-site/index.html'), picked('my-site/css/a.css')]);
		expect(out.map((f) => f.path)).toEqual(['index.html', 'css/a.css']);
	});

	it('leaves a site with a root index.html alone', () => {
		const out = stripWrapper([picked('index.html'), picked('docs/a.html')]);
		expect(out.map((f) => f.path)).toEqual(['index.html', 'docs/a.html']);
	});

	it('only strips when everything shares one top-level folder', () => {
		const out = stripWrapper([picked('a/index.html'), picked('b/x.html')]);
		expect(out.map((f) => f.path)).toEqual(['a/index.html', 'b/x.html']);
	});
});

describe('planSite', () => {
	it('accepts a folder with index.html and filters junk', () => {
		const plan = planSite([
			picked('site/index.html', 10),
			picked('site/.DS_Store', 5),
			picked('site/js/app.js', 20)
		]);
		expect(plan.ok).toBe(true);
		if (plan.ok) {
			expect(plan.files.map((f) => f.path)).toEqual(['index.html', 'js/app.js']);
			expect(plan.totalBytes).toBe(30);
		}
	});

	it('explains what is wrong', () => {
		expect(planSite([])).toEqual({ ok: false, reason: 'empty' });
		expect(planSite([picked('.DS_Store')])).toEqual({ ok: false, reason: 'empty' });
		expect(planSite([picked('site/about.html')])).toEqual({ ok: false, reason: 'noIndex' });
		const many = [picked('index.html')];
		for (let i = 0; i < SITE_MAX_ENTRIES; i++) many.push(picked(`p${i}.html`));
		expect(planSite(many)).toEqual({ ok: false, reason: 'tooMany' });
		expect(planSite([picked('index.html', 5 * 1024 * 1024 + 1)])).toEqual({
			ok: false,
			reason: 'tooLarge'
		});
	});
});
