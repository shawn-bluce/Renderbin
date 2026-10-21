/**
 * A file's format. 'site' is a whole directory (index.html plus the pages,
 * styles and scripts it uses) stored as one zip; it is created only through
 * `uploadSite`, never through `createFile`.
 */
export type FileKind = 'html' | 'markdown' | 'txt' | 'site';

/** One file inside a site. */
export interface SiteEntry {
	path: string;
	size: number;
}

export interface FileItem {
	slug: string;
	name: string;
	kind: FileKind;
	/** Stored source length in bytes, from the server's content_size column. */
	size: number;
	is_public: boolean;
	access_code: string;
	tags: string;
	created_at: string;
	updated_at: string;
	success_count: number;
	code_success_count: number;
	failure_count: number;
	html_content?: string;
	expires_at?: string | null;
	max_views?: number | null;
	view_count?: number;
	/**
	 * When a time or view limit last took this link offline, and which limit it
	 * was. Only the latest event is kept, and configuring a new limit clears it
	 * — so the row shows the badge only while the file is still private.
	 */
	expired_at?: string | null;
	expired_reason?: '' | 'ttl' | 'views';
	/** A site's files; only `getFile` and the site endpoints return them. */
	entries?: SiteEntry[];
}

import { api, apiVoid } from './http';

/** Every live file in one array -- what API clients get without `limit`. */
export function listFiles(f?: typeof fetch): Promise<FileItem[]> {
	return api('/api/files', { fetch: f });
}

export type SortKey = 'recent' | 'name' | 'success' | 'failure';
export type Visibility = 'all' | 'public' | 'private';

/** How many files each dashboard view holds; sent with every page. */
export interface FileCounts {
	all: number;
	public: number;
	private: number;
	trash: number;
}

export interface FilePage {
	files: FileItem[];
	/** Where the next page starts; empty on the last one. */
	next_cursor: string;
	counts: FileCounts;
}

export interface PageQuery {
	/** The trash instead of the live files; it has one order and no filter. */
	trash?: boolean;
	sort?: SortKey;
	dir?: 'asc' | 'desc';
	visibility?: Visibility;
	cursor?: string;
	limit?: number;
}

export const PAGE_SIZE = 100;

/**
 * One page of the listing, filtered and sorted by the server. Keyset pages:
 * pass the previous page's `next_cursor` to continue, with the same query --
 * the server refuses a cursor from a different listing.
 */
export function listFilesPage(q: PageQuery, f?: typeof fetch): Promise<FilePage> {
	const params = new URLSearchParams({ limit: String(q.limit ?? PAGE_SIZE) });
	if (q.trash) {
		params.set('deleted', 'true');
	} else {
		params.set('sort', q.sort ?? 'recent');
		if (q.dir) params.set('dir', q.dir);
		params.set('visibility', q.visibility ?? 'all');
	}
	if (q.cursor) params.set('cursor', q.cursor);
	return api(`/api/files?${params}`, { fetch: f });
}

// A search hit: a name match renders as a plain row; any other match also
// carries a snippet for weakened display -- the head field that matched (a
// page's title or description), or the content match ±100 runes, ellipsized.
export interface SearchResult extends FileItem {
	matched_name: boolean;
	matched_meta: boolean;
	matched_content: boolean;
	snippet?: string;
}

/**
 * Substring search over the current user's own files: names and head fields
 * (title, description, keywords) by default, the full text with `content`.
 *
 * Not `/api/files/search`: that shadowed any file whose custom slug happened to
 * be "search", since a static path segment wins over `{slug}` on the server.
 */
export function searchFiles(query: string, content: boolean): Promise<SearchResult[]> {
	const params = new URLSearchParams({ q: query });
	if (content) params.set('content', 'true');
	return api(`/api/search?${params}`);
}

export function createFile(name: string, htmlContent: string, kind: FileKind): Promise<FileItem> {
	return api('/api/files', { method: 'POST', json: { name, kind, html_content: htmlContent } });
}

export function getFile(slug: string): Promise<FileItem> {
	return api(`/api/files/${slug}`);
}

/**
 * Edits a file's name, slug and access code, and -- for a document -- its
 * content. A site takes no html_content here; see `replaceSite`.
 */
export function updateFile(
	slug: string,
	fields: { name: string; slug: string; html_content?: string; access_code: string }
): Promise<FileItem> {
	return api(`/api/files/${slug}`, { method: 'PATCH', json: fields });
}

export function renameFile(slug: string, name: string): Promise<FileItem> {
	return api(`/api/files/${slug}/name`, { method: 'PATCH', json: { name } });
}

export function restoreFile(slug: string): Promise<FileItem> {
	return api(`/api/files/${slug}/restore`, { method: 'POST' });
}

export function setExpiry(
	slug: string,
	fields: { ttl?: string | null; max_views?: number | null }
): Promise<FileItem> {
	return api(`/api/files/${slug}/expiry`, { method: 'PATCH', json: fields });
}

export function setVisibility(slug: string, isPublic: boolean): Promise<FileItem> {
	return api(`/api/files/${slug}/visibility`, { method: 'PATCH', json: { is_public: isPublic } });
}

export function setTags(slug: string, tags: string): Promise<FileItem> {
	return api(`/api/files/${slug}/tags`, { method: 'PATCH', json: { tags } });
}

export function refreshCode(slug: string): Promise<FileItem> {
	return api(`/api/files/${slug}/refresh-code`, { method: 'POST' });
}

export function deleteFile(slug: string): Promise<void> {
	return apiVoid(`/api/files/${slug}`, { method: 'DELETE' });
}

export function deleteFilePermanent(slug: string): Promise<void> {
	return apiVoid(`/api/files/${slug}/permanent`, { method: 'DELETE' });
}

/**
 * Permanently delete everything in the trash, returning how many files went.
 * Outside `/api/files` for the same reason as `searchFiles` — and it matters
 * more here, since this one deletes.
 */
export function emptyTrash(): Promise<{ deleted: number }> {
	return api('/api/trash', { method: 'DELETE' });
}

/**
 * Uploads a directory site as a zip. `name` may be null: the server then names
 * the site after the <title> of its index.html. Lives at /api/sites rather
 * than under /api/files for the same reason as `searchFiles`.
 */
export function uploadSite(name: string | null, zip: Blob): Promise<FileItem> {
	const query = name ? `?${new URLSearchParams({ name })}` : '';
	return api(`/api/sites${query}`, { method: 'POST', body: zip, type: 'application/zip' });
}

/** Replaces a site's whole content with a new zip. */
export function replaceSite(slug: string, zip: Blob): Promise<FileItem> {
	return api(`/api/files/${slug}/site`, { method: 'PUT', body: zip, type: 'application/zip' });
}
