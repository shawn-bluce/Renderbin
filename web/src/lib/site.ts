// Client-side preparation of a folder upload: the same shape checks the server
// applies in internal/site, run before anything is zipped or sent, so a folder
// without index.html is rejected on the spot instead of after a round trip.
// The server still validates everything; these mirror its rules, they do not
// replace them.

/** Mirrors site.MaxTotalBytes. */
export const SITE_MAX_BYTES = 5 * 1024 * 1024;
/** Mirrors site.MaxEntries. */
export const SITE_MAX_ENTRIES = 200;
/** Mirrors site.MaxUploadBytes, the cap on a zip request body. */
export const SITE_MAX_UPLOAD_BYTES = 8 * 1024 * 1024;

export interface PickedFile {
	/** Path relative to the picked or dropped folder, "/"-separated. */
	path: string;
	file: File;
}

export type SitePlan =
	| { ok: true; files: PickedFile[]; totalBytes: number }
	| { ok: false; reason: 'empty' | 'noIndex' | 'tooMany' | 'tooLarge' };

/** OS litter that never belongs in a site (mirrors site.isJunk). */
export function isJunkPath(path: string): boolean {
	const segs = path.split('/');
	if (segs.includes('__MACOSX')) return true;
	const base = segs[segs.length - 1];
	return (
		base.startsWith('._') || base === '.DS_Store' || base === 'Thumbs.db' || base === 'desktop.ini'
	);
}

/**
 * Removes one wrapping directory when there is no root index.html and every
 * file sits under the same top-level folder -- which is always the case for a
 * folder picked with webkitdirectory ("my-site/index.html").
 */
export function stripWrapper(files: PickedFile[]): PickedFile[] {
	if (files.length === 0 || files.some((f) => f.path === 'index.html')) return files;
	let prefix: string | null = null;
	for (const f of files) {
		const slash = f.path.indexOf('/');
		if (slash <= 0) return files;
		const top = f.path.slice(0, slash);
		if (prefix === null) prefix = top;
		else if (top !== prefix) return files;
	}
	return files.map((f) => ({ ...f, path: f.path.slice(prefix!.length + 1) }));
}

/** Filters, normalizes and checks a picked folder against the site limits. */
export function planSite(picked: PickedFile[]): SitePlan {
	const files = stripWrapper(
		picked
			.map((f) => ({ ...f, path: f.path.replace(/\\/g, '/').replace(/^\/+/, '') }))
			.filter((f) => f.path !== '' && !isJunkPath(f.path))
	);
	if (files.length === 0) return { ok: false, reason: 'empty' };
	if (!files.some((f) => f.path === 'index.html')) return { ok: false, reason: 'noIndex' };
	if (files.length > SITE_MAX_ENTRIES) return { ok: false, reason: 'tooMany' };
	const totalBytes = files.reduce((n, f) => n + f.file.size, 0);
	if (totalBytes > SITE_MAX_BYTES) return { ok: false, reason: 'tooLarge' };
	return { ok: true, files, totalBytes };
}

/** The files of an <input webkitdirectory> selection, with their paths. */
export function pickedFromInput(list: FileList): PickedFile[] {
	return Array.from(list).map((file) => ({ path: file.webkitRelativePath || file.name, file }));
}

/**
 * Reads every file under the dropped directory entries. The entries must be
 * taken from the DataTransfer synchronously, inside the drop handler -- the
 * item list is emptied as soon as the handler returns -- which is why this
 * takes entries rather than the event.
 */
export async function pickedFromEntries(entries: FileSystemEntry[]): Promise<PickedFile[]> {
	const out: PickedFile[] = [];
	async function walk(entry: FileSystemEntry, prefix: string): Promise<void> {
		const path = prefix + entry.name;
		if (entry.isFile) {
			const file = await new Promise<File>((resolve, reject) =>
				(entry as FileSystemFileEntry).file(resolve, reject)
			);
			out.push({ path, file });
			return;
		}
		if (!entry.isDirectory) return;
		const reader = (entry as FileSystemDirectoryEntry).createReader();
		// readEntries returns at most ~100 entries per call; keep calling until
		// it comes back empty.
		for (;;) {
			const batch = await new Promise<FileSystemEntry[]>((resolve, reject) =>
				reader.readEntries(resolve, reject)
			);
			if (batch.length === 0) break;
			for (const child of batch) await walk(child, path + '/');
		}
	}
	for (const entry of entries) await walk(entry, '');
	return out;
}
