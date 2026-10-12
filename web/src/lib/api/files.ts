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

// ApiError keeps the HTTP status so callers can map known failures (e.g. a
// 409 slug conflict) to translated messages instead of showing the backend's
// raw English text.
export class ApiError extends Error {
	constructor(
		public readonly status: number,
		message: string
	) {
		super(message);
		this.name = 'ApiError';
	}
}

async function throwApiError(res: Response): Promise<never> {
	// http.Error on the backend appends a trailing newline — trim it off.
	throw new ApiError(res.status, (await res.text()).trim());
}

async function unwrap<T>(res: Response): Promise<T> {
	if (!res.ok) {
		await throwApiError(res);
	}
	return res.json();
}

export function listFiles(): Promise<FileItem[]> {
	return fetch('/api/files').then((res) => unwrap<FileItem[]>(res));
}

// A search hit: a name match renders as a plain row; a content-only match
// also carries a snippet (the match ±100 runes, ellipsized) for weakened display.
export interface SearchResult extends FileItem {
	matched_name: boolean;
	matched_content: boolean;
	snippet?: string;
}

/**
 * Substring search over the current user's own files (name-only by default).
 *
 * Not `/api/files/search`: that shadowed any file whose custom slug happened to
 * be "search", since a static path segment wins over `{slug}` on the server.
 */
export function searchFiles(query: string, content: boolean): Promise<SearchResult[]> {
	const params = new URLSearchParams({ q: query });
	if (content) params.set('content', 'true');
	return fetch(`/api/search?${params}`).then((res) => unwrap<SearchResult[]>(res));
}

export function listTrashed(): Promise<FileItem[]> {
	return fetch('/api/files?deleted=true').then((res) => unwrap<FileItem[]>(res));
}

export function createFile(name: string, htmlContent: string, kind: FileKind): Promise<FileItem> {
	return fetch('/api/files', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ name, kind, html_content: htmlContent })
	}).then((res) => unwrap<FileItem>(res));
}

export function getFile(slug: string): Promise<FileItem> {
	return fetch(`/api/files/${slug}`).then((res) => unwrap<FileItem>(res));
}

/**
 * Edits a file's name, slug and access code, and -- for a document -- its
 * content. A site takes no html_content here; see `replaceSite`.
 */
export function updateFile(
	slug: string,
	fields: { name: string; slug: string; html_content?: string; access_code: string }
): Promise<FileItem> {
	return fetch(`/api/files/${slug}`, {
		method: 'PATCH',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(fields)
	}).then((res) => unwrap<FileItem>(res));
}

export function renameFile(slug: string, name: string): Promise<FileItem> {
	return fetch(`/api/files/${slug}/name`, {
		method: 'PATCH',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ name })
	}).then((res) => unwrap<FileItem>(res));
}

export function restoreFile(slug: string): Promise<FileItem> {
	return fetch(`/api/files/${slug}/restore`, { method: 'POST' }).then((res) =>
		unwrap<FileItem>(res)
	);
}

export function setExpiry(
	slug: string,
	fields: { ttl?: string | null; max_views?: number | null }
): Promise<FileItem> {
	return fetch(`/api/files/${slug}/expiry`, {
		method: 'PATCH',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(fields)
	}).then((res) => unwrap<FileItem>(res));
}

export function setVisibility(slug: string, isPublic: boolean): Promise<FileItem> {
	return fetch(`/api/files/${slug}/visibility`, {
		method: 'PATCH',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ is_public: isPublic })
	}).then((res) => unwrap<FileItem>(res));
}

export function setTags(slug: string, tags: string): Promise<FileItem> {
	return fetch(`/api/files/${slug}/tags`, {
		method: 'PATCH',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ tags })
	}).then((res) => unwrap<FileItem>(res));
}

export function refreshCode(slug: string): Promise<FileItem> {
	return fetch(`/api/files/${slug}/refresh-code`, { method: 'POST' }).then((res) =>
		unwrap<FileItem>(res)
	);
}

export async function deleteFile(slug: string): Promise<void> {
	const res = await fetch(`/api/files/${slug}`, { method: 'DELETE' });
	if (!res.ok) {
		await throwApiError(res);
	}
}

export async function deleteFilePermanent(slug: string): Promise<void> {
	const res = await fetch(`/api/files/${slug}/permanent`, { method: 'DELETE' });
	if (!res.ok) {
		await throwApiError(res);
	}
}

/**
 * Permanently delete everything in the trash, returning how many files went.
 * Outside `/api/files` for the same reason as `searchFiles` — and it matters
 * more here, since this one deletes.
 */
export function emptyTrash(): Promise<{ deleted: number }> {
	return fetch('/api/trash', { method: 'DELETE' }).then((res) => unwrap<{ deleted: number }>(res));
}

/**
 * Uploads a directory site as a zip. `name` may be null: the server then names
 * the site after the <title> of its index.html. Lives at /api/sites rather
 * than under /api/files for the same reason as `searchFiles`.
 */
export function uploadSite(name: string | null, zip: Blob): Promise<FileItem> {
	const query = name ? `?${new URLSearchParams({ name })}` : '';
	return fetch(`/api/sites${query}`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/zip' },
		body: zip
	}).then((res) => unwrap<FileItem>(res));
}

/** Replaces a site's whole content with a new zip. */
export function replaceSite(slug: string, zip: Blob): Promise<FileItem> {
	return fetch(`/api/files/${slug}/site`, {
		method: 'PUT',
		headers: { 'Content-Type': 'application/zip' },
		body: zip
	}).then((res) => unwrap<FileItem>(res));
}
