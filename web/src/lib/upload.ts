// What the dashboard does with files the user picks or drops: tell documents
// from zips from junk, and turn a document into a file. The page owns the
// progress and the error messages.
import { createFile, type FileItem, type FileKind } from '$lib/api/files';
import { MAX_HTML_BYTES } from '$lib/files-ui';

/** Supported document formats, by extension. The server decides how each kind is rendered. */
const KIND_EXTENSION = /\.(html?|md|markdown|te?xt)$/i;

type Named = { name: string; type: string };

/** A document's format from its extension, falling back to its MIME type. */
export function detectKind(f: Named): FileKind | null {
	if (/\.html?$/i.test(f.name)) return 'html';
	if (/\.(md|markdown)$/i.test(f.name)) return 'markdown';
	if (/\.te?xt$/i.test(f.name)) return 'txt';
	if (f.type === 'text/html') return 'html';
	if (f.type === 'text/markdown') return 'markdown';
	if (f.type === 'text/plain') return 'txt';
	return null;
}

export function isZip(f: Named): boolean {
	return /\.zip$/i.test(f.name) || f.type === 'application/zip';
}

export interface UploadPlan {
	/** Documents to upload one by one. */
	documents: { file: File; kind: FileKind }[];
	/** Each zip is a whole site and goes to the site endpoint as-is. */
	zips: File[];
	/** Not a supported format at all. */
	skipped: number;
	/** A supported format, but over the per-document size cap. */
	oversize: number;
}

/** Sorts a pick or a drop into what will be uploaded and what will be left behind. */
export function planUpload(all: File[]): UploadPlan {
	const zips = all.filter(isZip);
	const rest = all.filter((f) => !isZip(f));
	const typed = rest
		.map((file) => ({ file, kind: detectKind(file) }))
		.filter((c): c is { file: File; kind: FileKind } => c.kind !== null);
	const documents = typed.filter((c) => c.file.size <= MAX_HTML_BYTES);
	return {
		documents,
		zips,
		skipped: rest.length - typed.length,
		oversize: typed.length - documents.length
	};
}

function extractHtmlTitle(content: string): string {
	const doc = new DOMParser().parseFromString(content, 'text/html');
	return doc.querySelector('title')?.textContent?.trim() ?? '';
}

/**
 * Uploads one document. Its name is the HTML <title> (html kind) or the
 * filename; either can be edited inline in the list afterwards.
 */
export async function uploadDocument(file: File, kind: FileKind): Promise<FileItem> {
	const content = await file.text();
	const name =
		(kind === 'html' ? extractHtmlTitle(content) : '') || file.name.replace(KIND_EXTENSION, '');
	return createFile(name, content, kind);
}
