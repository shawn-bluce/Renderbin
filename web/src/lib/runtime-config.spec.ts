import { describe, expect, it } from 'vitest';
import { fileSizeLimitLabel, publicShareURL } from './runtime-config';

describe('runtime upload and sharing configuration', () => {
	it('formats the backend-provided upload limit for UI messages', () => {
		expect(fileSizeLimitLabel(20 * 1024 * 1024)).toBe('20MB');
	});

	it('uses the configured public share origin', () => {
		expect(
			publicShareURL('https://share.example.com', 'https://admin.example.com', 'doc', 'secret')
		).toBe('https://share.example.com/res/doc?code=secret');
	});

	it('keeps the current-origin behavior when no share origin is configured', () => {
		expect(publicShareURL('', 'https://admin.example.com', 'doc', 'secret')).toBe(
			'https://admin.example.com/res/doc?code=secret'
		);
	});
});
