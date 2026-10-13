// Small presentation helpers shared by the dashboard and its file modals. Pure
// functions over FileItem / Date; nothing here owns state.
import { t } from '$lib/i18n/index.svelte';
import type { MessageKey } from '$lib/i18n/messages';
import { buildZip } from '$lib/zip';
import {
	planSite,
	SITE_MAX_ENTRIES,
	SITE_MAX_UPLOAD_BYTES,
	type PickedFile,
	type SitePlan
} from '$lib/site';
import type { FileItem, FileKind } from '$lib/api/files';

/** The API's per-document ceiling; mirrors the server so a refusal is local and instant. */
export const MAX_HTML_BYTES = 5 * 1024 * 1024;

/** Why a document's content cannot be saved, as a message key -- or null if it can. */
export function validateDoc(content: string): MessageKey | null {
	if (!content) return 'error.contentRequired';
	if (new Blob([content]).size > MAX_HTML_BYTES) return 'error.contentTooLarge';
	return null;
}

/** The message for a zip over the upload cap, or null if it fits. */
export function zipTooLarge(zip: Blob & { name?: string }): string | null {
	return zip.size > SITE_MAX_UPLOAD_BYTES
		? t('site.error.zipTooLarge', { name: zip.name ?? '' })
		: null;
}

/** Moves keyboard focus to the element when it appears (inline editors). */
export function focusOnMount(node: HTMLElement) {
	node.focus();
}

export function resUrl(file: FileItem): string {
	return `${location.origin}/res/${file.slug}?code=${file.access_code}`;
}

export function kindIcon(kind: FileKind): string {
	if (kind === 'markdown') return 'lucide:file-type';
	if (kind === 'txt') return 'lucide:file-text';
	if (kind === 'site') return 'lucide:folder';
	return 'lucide:file-code-2';
}

export function kindLabel(kind: FileKind): string {
	if (kind === 'markdown') return 'MD';
	if (kind === 'txt') return 'TXT';
	if (kind === 'site') return 'SITE';
	return 'HTML';
}

export function downloadName(file: FileItem): string {
	const ext =
		file.kind === 'markdown'
			? 'md'
			: file.kind === 'txt'
				? 'txt'
				: file.kind === 'site'
					? 'zip'
					: 'html';
	return `${file.name}.${ext}`;
}

function sitePlanError(plan: Exclude<SitePlan, { ok: true }>): string {
	switch (plan.reason) {
		case 'empty':
			return t('site.error.empty');
		case 'noIndex':
			return t('site.error.noIndex');
		case 'tooMany':
			return t('site.error.tooMany', { n: SITE_MAX_ENTRIES });
		case 'tooLarge':
			return t('site.error.tooLarge');
	}
}

/**
 * A picked or dropped folder, checked against the site rules and zipped for
 * upload -- or the reason it cannot be. The server re-checks all of it.
 */
export async function zipFolder(picked: PickedFile[]): Promise<Blob | string> {
	const plan = planSite(picked);
	if (!plan.ok) return sitePlanError(plan);
	const entries = await Promise.all(
		plan.files.map(async (f) => ({
			path: f.path,
			data: new Uint8Array(await f.file.arrayBuffer())
		}))
	);
	return new Blob([buildZip(entries)], { type: 'application/zip' });
}
