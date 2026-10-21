import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from './http';
import {
	createFile,
	deleteFile,
	deleteFilePermanent,
	emptyTrash,
	getFile,
	listFiles,
	listTrashed,
	refreshCode,
	renameFile,
	restoreFile,
	searchFiles,
	setExpiry,
	setTags,
	setVisibility,
	updateFile
} from './files';

function okJson(body: unknown): Response {
	return {
		ok: true,
		status: 200,
		json: async () => body,
		text: async () => JSON.stringify(body)
	} as unknown as Response;
}

function failure(status: number, text: string): Response {
	return {
		ok: false,
		status,
		json: async () => ({}),
		text: async () => text
	} as unknown as Response;
}

function mockFetch(res: Response) {
	const fn = vi.fn().mockResolvedValue(res);
	vi.stubGlobal('fetch', fn);
	return fn;
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe('files api — request shapes', () => {
	it('listFiles GETs /api/files', async () => {
		const fetchMock = mockFetch(okJson([{ slug: 'a' }]));
		const result = await listFiles();
		expect(fetchMock).toHaveBeenCalledWith('/api/files');
		expect(result).toEqual([{ slug: 'a' }]);
	});

	it('listTrashed GETs /api/files?deleted=true', async () => {
		const fetchMock = mockFetch(okJson([]));
		await listTrashed();
		expect(fetchMock).toHaveBeenCalledWith('/api/files?deleted=true');
	});

	it('createFile POSTs name + kind + snake_case html_content', async () => {
		const fetchMock = mockFetch(okJson({ slug: 'x' }));
		await createFile('My Doc', '# hi', 'markdown');
		expect(fetchMock).toHaveBeenCalledWith(
			'/api/files',
			expect.objectContaining({
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ name: 'My Doc', kind: 'markdown', html_content: '# hi' })
			})
		);
	});

	it('getFile GETs /api/files/{slug}', async () => {
		const fetchMock = mockFetch(okJson({ slug: 'abc', html_content: '<p>x</p>' }));
		const result = await getFile('abc');
		expect(fetchMock).toHaveBeenCalledWith('/api/files/abc');
		expect(result.html_content).toBe('<p>x</p>');
	});

	it('updateFile PATCHes /api/files/{slug} with the given fields', async () => {
		const fetchMock = mockFetch(okJson({ slug: 'new' }));
		await updateFile('old', {
			name: 'n',
			slug: 'new',
			html_content: '<p>v2</p>',
			access_code: 'c'
		});
		expect(fetchMock).toHaveBeenCalledWith(
			'/api/files/old',
			expect.objectContaining({
				method: 'PATCH',
				body: JSON.stringify({
					name: 'n',
					slug: 'new',
					html_content: '<p>v2</p>',
					access_code: 'c'
				})
			})
		);
	});

	it('renameFile PATCHes /api/files/{slug}/name with the new name', async () => {
		const fetchMock = mockFetch(okJson({ slug: 'a', name: 'renamed' }));
		await renameFile('a', 'renamed');
		expect(fetchMock).toHaveBeenCalledWith(
			'/api/files/a/name',
			expect.objectContaining({
				method: 'PATCH',
				body: JSON.stringify({ name: 'renamed' })
			})
		);
	});

	it('restoreFile POSTs /api/files/{slug}/restore', async () => {
		const fetchMock = mockFetch(okJson({ slug: 'a' }));
		await restoreFile('a');
		expect(fetchMock).toHaveBeenCalledWith('/api/files/a/restore', { method: 'POST' });
	});

	it('setExpiry PATCHes /api/files/{slug}/expiry with the ttl/max_views payload', async () => {
		const fetchMock = mockFetch(okJson({ slug: 'a' }));
		await setExpiry('a', { max_views: 5 });
		expect(fetchMock).toHaveBeenCalledWith(
			'/api/files/a/expiry',
			expect.objectContaining({
				method: 'PATCH',
				body: JSON.stringify({ max_views: 5 })
			})
		);
	});

	it('setVisibility PATCHes /api/files/{slug}/visibility with is_public', async () => {
		const fetchMock = mockFetch(okJson({ slug: 'a', is_public: true }));
		await setVisibility('a', true);
		expect(fetchMock).toHaveBeenCalledWith(
			'/api/files/a/visibility',
			expect.objectContaining({
				method: 'PATCH',
				body: JSON.stringify({ is_public: true })
			})
		);
	});

	it('setTags PATCHes /api/files/{slug}/tags', async () => {
		const fetchMock = mockFetch(okJson({ slug: 'a', tags: 'x' }));
		await setTags('a', 'x,y');
		expect(fetchMock).toHaveBeenCalledWith(
			'/api/files/a/tags',
			expect.objectContaining({
				method: 'PATCH',
				body: JSON.stringify({ tags: 'x,y' })
			})
		);
	});

	it('refreshCode POSTs /api/files/{slug}/refresh-code', async () => {
		const fetchMock = mockFetch(okJson({ slug: 'a', access_code: 'new' }));
		await refreshCode('a');
		expect(fetchMock).toHaveBeenCalledWith('/api/files/a/refresh-code', { method: 'POST' });
	});

	it('deleteFile DELETEs /api/files/{slug} and resolves void', async () => {
		const fetchMock = mockFetch(okJson({}));
		await expect(deleteFile('a')).resolves.toBeUndefined();
		expect(fetchMock).toHaveBeenCalledWith('/api/files/a', { method: 'DELETE' });
	});

	it('deleteFilePermanent DELETEs /api/files/{slug}/permanent and resolves void', async () => {
		const fetchMock = mockFetch(okJson({}));
		await expect(deleteFilePermanent('a')).resolves.toBeUndefined();
		expect(fetchMock).toHaveBeenCalledWith('/api/files/a/permanent', { method: 'DELETE' });
	});

	// Deliberately not /api/files/trash: a static segment there would shadow a
	// file whose custom slug is "trash", and this request deletes.
	it('emptyTrash DELETEs /api/trash and returns the count', async () => {
		const fetchMock = mockFetch(okJson({ deleted: 3 }));
		await expect(emptyTrash()).resolves.toEqual({ deleted: 3 });
		expect(fetchMock).toHaveBeenCalledWith('/api/trash', { method: 'DELETE' });
	});
});

describe('files api — error handling', () => {
	it('unwrap throws the response body text on non-ok', async () => {
		mockFetch(failure(500, 'boom'));
		await expect(listFiles()).rejects.toThrow('boom');
	});

	it('deleteFile throws the response body text on non-ok', async () => {
		mockFetch(failure(409, 'cannot delete'));
		await expect(deleteFile('a')).rejects.toThrow('cannot delete');
	});

	it('throws an ApiError carrying the status, with the message trimmed', async () => {
		mockFetch(failure(409, 'slug already taken\n'));
		const err = await listFiles().catch((e: unknown) => e);
		expect(err).toBeInstanceOf(ApiError);
		expect((err as ApiError).status).toBe(409);
		expect((err as ApiError).message).toBe('slug already taken');
	});
});

describe('searchFiles', () => {
	it('GETs name-only search without the content flag', async () => {
		const fetchMock = mockFetch(okJson([]));
		await expect(searchFiles('hello', false)).resolves.toEqual([]);
		expect(fetchMock).toHaveBeenCalledWith('/api/search?q=hello');
	});

	it('adds content=true and encodes the query', async () => {
		const results = [
			{
				slug: 'a',
				matched_name: false,
				matched_meta: false,
				matched_content: true,
				snippet: '…hello…'
			}
		];
		const fetchMock = mockFetch(okJson(results));
		await expect(searchFiles('a b&c', true)).resolves.toEqual(results);
		expect(fetchMock).toHaveBeenCalledWith('/api/search?q=a+b%26c&content=true');
	});

	it('throws ApiError on failure', async () => {
		mockFetch(failure(500, 'internal error'));
		await expect(searchFiles('x', false)).rejects.toBeInstanceOf(ApiError);
	});
});
