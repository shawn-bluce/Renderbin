<script lang="ts">
	// All Date/Map values in this file are ephemeral formatting/grouping locals — computed,
	// consumed, and discarded within a single derivation or event handler, never stored in
	// $state or mutated afterwards — so SvelteDate/SvelteMap's extra reactivity isn't needed.
	/* eslint-disable svelte/prefer-svelte-reactivity */
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import Icon from '@iconify/svelte';
	import { logout } from '$lib/api/auth';
	import { t, intlLocale } from '$lib/i18n/index.svelte';
	import type { MessageKey } from '$lib/i18n/messages';
	import LanguageSwitcher from '$lib/components/LanguageSwitcher.svelte';
	import { copyText } from '$lib/clipboard';
	import { formatSize } from '$lib/format';
	import { fileSizeLimitLabel, publicShareURL } from '$lib/runtime-config';
	import { getUsage, type Usage } from '$lib/api/settings';
	import {
		ApiError,
		createFile,
		deleteFile,
		deleteFilePermanent,
		emptyTrash,
		getFile,
		listTrashed,
		refreshCode,
		renameFile,
		restoreFile,
		searchFiles,
		setExpiry,
		setTags,
		setVisibility,
		updateFile,
		type FileItem,
		type FileKind,
		type SearchResult
	} from '$lib/api/files';

	let { data } = $props();

	// Intentional one-time snapshot: `files` is mutated locally for optimistic
	// updates and shouldn't re-sync if `data` changes.
	// svelte-ignore state_referenced_locally
	let files = $state<FileItem[]>(data.files);

	// Intentional one-time snapshot: runtime config is fixed for the process.
	// svelte-ignore state_referenced_locally
	const maxFileSizeBytes = data.maxFileSizeBytes;
	const maxFileSizeLabel = fileSizeLimitLabel(maxFileSizeBytes);

	let uploading = $state(false);
	let errorMessage = $state<string | null>(null);
	let fileInput = $state<HTMLInputElement | null>(null);
	let isDraggingOver = $state(false);
	let dragCounter = 0;

	let tab = $state<'files' | 'trashed'>('files');
	let trashedFiles = $state<FileItem[]>([]);
	let trashedLoaded = $state(false);
	let trashLoading = $state(false);
	let emptyingTrash = $state(false);

	// New-file modal. `kind` is only ever chosen here: the format decides how
	// /res/{slug} renders the source and is fixed at creation, so the edit modal
	// offers no way to change it.
	const kindOptions = [
		{ key: 'html', labelKey: 'kind.html' },
		{ key: 'markdown', labelKey: 'kind.markdown' },
		{ key: 'txt', labelKey: 'kind.txt' }
	] as const satisfies { key: FileKind; labelKey: MessageKey }[];
	let createOpen = $state(false);
	let createName = $state('');
	let createKind = $state<FileKind>('markdown');
	let createContent = $state('');
	let createBusy = $state(false);
	let createError = $state<string | null>(null);

	const visibilityOptions = [
		{ key: 'all', labelKey: 'visibility.all' },
		{ key: 'public', labelKey: 'visibility.public' },
		{ key: 'private', labelKey: 'visibility.private' }
	] as const satisfies { key: string; labelKey: MessageKey }[];

	// API-backed search over the current user's own files: 250ms debounce,
	// name-only by default, optionally including file contents. A non-empty
	// query swaps the list for the (flat) result view.
	let search = $state('');
	let contentSearch = $state(false);
	let searchResults = $state<SearchResult[] | null>(null);
	let searchLoading = $state(false);
	let searchTimer: ReturnType<typeof setTimeout> | undefined;
	let searchSeq = 0; // invalidates in-flight responses (stale results are dropped)
	const searchActive = $derived(search.trim().length > 0);

	function scheduleSearch() {
		clearTimeout(searchTimer);
		searchSeq++;
		if (!search.trim()) {
			searchResults = null;
			searchLoading = false;
			return;
		}
		searchLoading = true;
		searchTimer = setTimeout(runSearch, 250);
	}

	async function runSearch() {
		const q = search.trim();
		if (!q) return;
		const seq = ++searchSeq;
		searchLoading = true;
		try {
			const results = await searchFiles(q, contentSearch);
			if (seq !== searchSeq) return;
			searchResults = results;
		} catch {
			if (seq !== searchSeq) return;
			searchResults = [];
			errorMessage = t('error.search');
		} finally {
			if (seq === searchSeq) searchLoading = false;
		}
	}

	function toggleContentSearch() {
		contentSearch = !contentSearch;
		if (search.trim()) {
			clearTimeout(searchTimer);
			runSearch();
		}
	}

	let visibilityFilter = $state<'all' | 'public' | 'private'>('all');
	let copiedSlug = $state<string | null>(null);
	let copiedTimeout: ReturnType<typeof setTimeout> | undefined;
	// The URL a copy attempt failed on. Both clipboard paths can be refused
	// (an old browser on a plain-HTTP LAN, or a denied permission), and a share
	// link the user cannot reach any other way is worse than an ugly one they
	// can -- so on failure the row renders it as selectable text instead.
	let copyFallbackUrl = $state<string | null>(null);

	// Storage use against the account's quota. Fetched separately from the file
	// list because it counts trashed files too, which is what the server
	// enforces on upload; summing the visible rows would show a smaller number
	// than the one that rejects the next upload.
	let usage = $state<Usage | null>(null);
	$effect(() => {
		getUsage()
			.then((u) => (usage = u))
			.catch(() => (usage = null));
	});

	type SortKey = 'recent' | 'name' | 'success' | 'failure';
	const sortOptions: { key: SortKey; labelKey: MessageKey; defaultDir: 'asc' | 'desc' }[] = [
		{ key: 'recent', labelKey: 'sort.recent', defaultDir: 'desc' },
		{ key: 'name', labelKey: 'sort.name', defaultDir: 'asc' },
		{ key: 'success', labelKey: 'sort.success', defaultDir: 'desc' },
		{ key: 'failure', labelKey: 'sort.failure', defaultDir: 'desc' }
	];
	let sortKey = $state<SortKey>('recent');
	let sortDir = $state<'asc' | 'desc'>('desc');

	function onSortClick(opt: (typeof sortOptions)[number]) {
		if (sortKey === opt.key) {
			if (opt.key !== 'recent') sortDir = sortDir === 'asc' ? 'desc' : 'asc';
		} else {
			sortKey = opt.key;
			sortDir = opt.defaultDir;
		}
	}

	// Full-edit modal (title / slug / access code / HTML source).
	let editOpen = $state(false);
	let editFile = $state<FileItem | null>(null);
	let editTitle = $state('');
	let editSlug = $state('');
	let editCode = $state('');
	let editContent = $state('');
	let editContentOpen = $state(false);
	let editBusy = $state(false);
	let editError = $state<string | null>(null);

	// Expiry modal. A ttl is an amount plus a unit ("1d", "36h", "6mo"), the
	// same grammar the API speaks, so the picker is free-form rather than a
	// fixed preset list.
	const ttlUnits = [
		{ key: 'h', labelKey: 'ttl.hours' },
		{ key: 'd', labelKey: 'ttl.days' },
		{ key: 'w', labelKey: 'ttl.weeks' },
		{ key: 'mo', labelKey: 'ttl.months' },
		{ key: 'y', labelKey: 'ttl.years' }
	] as const satisfies { key: string; labelKey: MessageKey }[];
	type TtlUnit = (typeof ttlUnits)[number]['key'];
	// Mirrors handlers.maxTTLYears — the client-side check exists only to give
	// a better message than the server's 400; the server still decides.
	const MAX_TTL_YEARS = 10;
	let expiryOpen = $state(false);
	let expiryFile = $state<FileItem | null>(null);
	let expiryMode = $state<'none' | 'time' | 'views'>('none');
	let expiryTtlValue = $state<number>(1);
	let expiryTtlUnit = $state<TtlUnit>('d');
	let expiryViews = $state<number>(10);
	let expiryBusy = $state(false);
	let expiryError = $state<string | null>(null);

	// Stamped when the modal opens so the preview below is computed from one
	// fixed "now" rather than drifting between keystrokes.
	let expiryNow = $state(0);

	// The deadline the current amount/unit would produce, for the preview line
	// under the picker. Calendar units step the calendar (a month later is the
	// same day of the next month), matching the server's AddDate arithmetic.
	// null means "the server would reject this", i.e. nothing to preview.
	function ttlDeadline(value: number, unit: TtlUnit, base: number): Date | null {
		if (!Number.isInteger(value) || value <= 0) return null;
		const d = new Date(base);
		if (unit === 'h') d.setHours(d.getHours() + value);
		else if (unit === 'd') d.setDate(d.getDate() + value);
		else if (unit === 'w') d.setDate(d.getDate() + value * 7);
		else if (unit === 'mo') d.setMonth(d.getMonth() + value);
		else d.setFullYear(d.getFullYear() + value);
		const cap = new Date(base);
		cap.setFullYear(cap.getFullYear() + MAX_TTL_YEARS);
		return d > cap ? null : d;
	}

	const expiryPreview = $derived(
		expiryMode === 'time' ? ttlDeadline(expiryTtlValue, expiryTtlUnit, expiryNow) : null
	);

	let tagInputSlug = $state<string | null>(null);
	let tagInputValue = $state('');

	// Inline title editing in the file list (same pattern as the tag input).
	let titleEditSlug = $state<string | null>(null);
	let titleEditValue = $state('');

	function autofocus(node: HTMLElement) {
		node.focus();
	}

	function fileTags(file: FileItem): string[] {
		return file.tags
			? file.tags
					.split(',')
					.map((t) => t.trim())
					.filter(Boolean)
			: [];
	}

	const totalCount = $derived(files.length);
	const publicCount = $derived(files.filter((f) => f.is_public).length);
	const privateCount = $derived(totalCount - publicCount);

	function passesVisibility(f: FileItem): boolean {
		if (visibilityFilter === 'public' && !f.is_public) return false;
		if (visibilityFilter === 'private' && f.is_public) return false;
		return true;
	}

	const filteredFiles = $derived(files.filter(passesVisibility));

	// Grouped-by-day view, used only for the default "Recent" sort.
	const groups = $derived.by(() => {
		const order: string[] = [];
		const byLabel = new Map<string, FileItem[]>();
		for (const file of filteredFiles) {
			const label = dayLabel(file.created_at);
			if (!byLabel.has(label)) {
				byLabel.set(label, []);
				order.push(label);
			}
			byLabel.get(label)!.push(file);
		}
		return order.map((label) => ({ label, items: byLabel.get(label)! }));
	});

	function compareFiles(a: FileItem, b: FileItem): number {
		const dir = sortDir === 'asc' ? 1 : -1;
		if (sortKey === 'name') return dir * a.name.localeCompare(b.name, intlLocale());
		if (sortKey === 'success') return dir * (a.success_count - b.success_count);
		if (sortKey === 'failure') return dir * (a.failure_count - b.failure_count);
		return 0;
	}

	// Flat sorted view, used for every sort mode except "Recent" — grouping by
	// day and sorting by name/success/failure at the same time isn't coherent,
	// so picking an explicit sort key drops the day grouping entirely.
	const sortedFlat = $derived([...filteredFiles].sort(compareFiles));

	// Search result rows, joined against `files` so optimistic local mutations
	// (rename, delete, visibility, …) show up immediately without re-querying.
	// A title match renders as a plain row; a content-only match carries the
	// weakened snippet. Always flat (never day-grouped).
	const searchRows = $derived.by(() => {
		if (!searchResults) return [];
		const bySlug = new Map(files.map((f) => [f.slug, f]));
		const rows: { file: FileItem; snippet: string | null }[] = [];
		for (const r of searchResults) {
			const file = bySlug.get(r.slug);
			if (!file || !passesVisibility(file)) continue;
			rows.push({ file, snippet: r.matched_name ? null : r.snippet || null });
		}
		return rows.sort((a, b) => compareFiles(a.file, b.file));
	});

	function isSameDay(a: Date, b: Date): boolean {
		return (
			a.getFullYear() === b.getFullYear() &&
			a.getMonth() === b.getMonth() &&
			a.getDate() === b.getDate()
		);
	}

	function dayLabel(iso: string): string {
		const d = new Date(iso);
		const now = new Date();
		if (isSameDay(d, now)) return t('date.today');
		const yesterday = new Date(now);
		yesterday.setDate(now.getDate() - 1);
		if (isSameDay(d, yesterday)) return t('date.yesterday');
		return d.toLocaleDateString(intlLocale(), {
			month: 'short',
			day: 'numeric',
			year: d.getFullYear() !== now.getFullYear() ? 'numeric' : undefined
		});
	}

	function formatTime(iso: string): string {
		return new Date(iso).toLocaleTimeString(intlLocale(), { hour: '2-digit', minute: '2-digit' });
	}

	// A file's expiry deadline, to the minute — a link that dies at 18:00 today
	// and one that dies at 06:00 today are not the same thing, so the row shows
	// the time and not just the date. The year is left off unless it differs
	// from the current one, same as dayLabel.
	function formatDeadline(iso: string): string {
		const d = new Date(iso);
		return d.toLocaleString(intlLocale(), {
			year: d.getFullYear() !== new Date().getFullYear() ? 'numeric' : undefined,
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit'
		});
	}

	function resUrl(file: FileItem): string {
		return publicShareURL(data.publicShareBaseURL, location.origin, file.slug, file.access_code);
	}

	async function copyToClipboard(text: string, slug: string) {
		copyFallbackUrl = null;
		if (!(await copyText(text))) {
			copyFallbackUrl = text;
			return;
		}
		copiedSlug = slug;
		clearTimeout(copiedTimeout);
		copiedTimeout = setTimeout(() => (copiedSlug = null), 1500);
	}

	// Supported upload formats, matched by filename extension (with a MIME-type
	// fallback). The server decides how each kind is rendered at /res/{slug}.
	const KIND_EXTENSION = /\.(html?|md|markdown|te?xt)$/i;

	function detectKind(f: File): FileKind | null {
		if (/\.html?$/i.test(f.name)) return 'html';
		if (/\.(md|markdown)$/i.test(f.name)) return 'markdown';
		if (/\.te?xt$/i.test(f.name)) return 'txt';
		if (f.type === 'text/html') return 'html';
		if (f.type === 'text/markdown') return 'markdown';
		if (f.type === 'text/plain') return 'txt';
		return null;
	}

	function kindIcon(kind: FileKind): string {
		if (kind === 'markdown') return 'lucide:file-type';
		if (kind === 'txt') return 'lucide:file-text';
		return 'lucide:file-code-2';
	}

	function kindLabel(kind: FileKind): string {
		if (kind === 'markdown') return 'MD';
		if (kind === 'txt') return 'TXT';
		return 'HTML';
	}

	function downloadName(file: FileItem): string {
		const ext = file.kind === 'markdown' ? 'md' : file.kind === 'txt' ? 'txt' : 'html';
		return `${file.name}.${ext}`;
	}

	function sourceLabel(kind: FileKind | undefined): string {
		if (kind === 'markdown') return t('source.markdown');
		if (kind === 'txt') return t('source.txt');
		return t('source.html');
	}

	function extractHtmlTitle(content: string): string {
		const doc = new DOMParser().parseFromString(content, 'text/html');
		return doc.querySelector('title')?.textContent?.trim() ?? '';
	}

	async function uploadOne(file: File, kind: FileKind): Promise<FileItem> {
		const content = await file.text();
		const name =
			(kind === 'html' ? extractHtmlTitle(content) : '') || file.name.replace(KIND_EXTENSION, '');
		return createFile(name, content, kind);
	}

	// Shared by the file picker (single or multi-select) and drag-and-drop
	// (single or multi-file). Names come from the HTML <title> (html kind)
	// or the filename; they can be edited inline in the list afterwards.
	async function uploadMany(fileList: FileList | File[]) {
		if (uploading) return;

		const all = Array.from(fileList);
		const candidates = all
			.map((file) => ({ file, kind: detectKind(file) }))
			.filter((c): c is { file: File; kind: FileKind } => c.kind !== null);
		const skippedCount = all.length - candidates.length;
		const accepted = candidates.filter((c) => c.file.size <= maxFileSizeBytes);
		const oversizeCount = candidates.length - accepted.length;
		if (accepted.length === 0) {
			errorMessage =
				oversizeCount > 0
					? t('error.oversizeOnly', { n: oversizeCount, size: maxFileSizeLabel })
					: t('error.noSupported');
			return;
		}

		errorMessage = null;
		uploading = true;
		const created: FileItem[] = [];
		let failedCount = 0;
		try {
			for (const { file, kind } of accepted) {
				try {
					created.push(await uploadOne(file, kind));
				} catch {
					failedCount++;
				}
			}
		} finally {
			uploading = false;
		}

		if (created.length > 0) {
			files = [...created, ...files];
		}

		const notes: string[] = [];
		if (skippedCount > 0) {
			notes.push(t('note.skipped', { n: skippedCount }));
		}
		if (oversizeCount > 0) {
			notes.push(t('note.oversize', { n: oversizeCount, size: maxFileSizeLabel }));
		}
		if (failedCount > 0) {
			notes.push(t('note.failed', { n: failedCount }));
		}
		if (notes.length > 0) {
			errorMessage = notes.join('; ') + '.';
		}
	}

	async function onFileSelected(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		if (!input.files || input.files.length === 0) return;
		await uploadMany(input.files);
		input.value = '';
	}

	function onDragEnter(e: DragEvent) {
		if (!e.dataTransfer?.types.includes('Files')) return;
		e.preventDefault();
		dragCounter++;
		isDraggingOver = true;
	}

	function onDragOver(e: DragEvent) {
		if (!e.dataTransfer?.types.includes('Files')) return;
		e.preventDefault();
	}

	function onDragLeave(e: DragEvent) {
		e.preventDefault();
		dragCounter = Math.max(0, dragCounter - 1);
		if (dragCounter === 0) isDraggingOver = false;
	}

	async function onDrop(e: DragEvent) {
		e.preventDefault();
		dragCounter = 0;
		isDraggingOver = false;
		const dropped = e.dataTransfer?.files;
		if (!dropped || dropped.length === 0) return;
		await uploadMany(dropped);
	}

	async function switchTab(next: 'files' | 'trashed') {
		tab = next;
		if (next === 'trashed' && !trashedLoaded && !trashLoading) {
			trashLoading = true;
			try {
				trashedFiles = await listTrashed();
				trashedLoaded = true;
			} catch (err) {
				errorMessage = err instanceof Error ? err.message : t('error.loadTrash');
			} finally {
				trashLoading = false;
			}
		}
	}

	async function onEmptyTrash() {
		if (trashedFiles.length === 0 || emptyingTrash) return;
		if (!confirm(t('confirm.emptyTrash', { n: trashedFiles.length }))) return;
		errorMessage = null;
		emptyingTrash = true;
		try {
			await emptyTrash();
			trashedFiles = [];
			await refreshUsage();
		} catch (err) {
			errorMessage = err instanceof Error ? err.message : t('error.emptyTrash');
		} finally {
			emptyingTrash = false;
		}
	}

	function openCreate() {
		createName = '';
		createKind = 'markdown';
		createContent = '';
		createError = null;
		createOpen = true;
	}

	function closeCreate() {
		createOpen = false;
	}

	async function submitCreate() {
		if (!createContent) {
			createError = t('error.contentRequired');
			return;
		}
		if (new Blob([createContent]).size > maxFileSizeBytes) {
			createError = t('error.contentTooLarge', { size: maxFileSizeLabel });
			return;
		}
		createError = null;
		createBusy = true;
		try {
			const created = await createFile(
				createName.trim() || t('untitled'),
				createContent,
				createKind
			);
			files = [created, ...files];
			await refreshUsage();
			// A new file lands at the top of "Recent"; leaving a search or a
			// visibility filter on would hide it and read as a failed create.
			search = '';
			searchResults = null;
			visibilityFilter = 'all';
			tab = 'files';
			closeCreate();
		} catch (err) {
			createError = err instanceof Error ? err.message : t('error.create');
		} finally {
			createBusy = false;
		}
	}

	async function onRestore(file: FileItem) {
		errorMessage = null;
		try {
			const restored = await restoreFile(file.slug);
			trashedFiles = trashedFiles.filter((f) => f.slug !== file.slug);
			files = [restored, ...files];
		} catch (err) {
			errorMessage = err instanceof Error ? err.message : t('error.restore');
		}
	}

	async function openEdit(file: FileItem) {
		editFile = file;
		editTitle = file.name;
		editSlug = file.slug;
		editCode = file.access_code;
		editContent = '';
		editContentOpen = false;
		editError = null;
		editBusy = true;
		editOpen = true;
		try {
			const full = await getFile(file.slug);
			editContent = full.html_content ?? '';
		} catch (err) {
			editError = err instanceof Error ? err.message : t('error.loadContent');
		} finally {
			editBusy = false;
		}
	}

	function closeEdit() {
		editOpen = false;
		editFile = null;
	}

	async function saveEdit() {
		if (!editFile) return;
		const name = editTitle.trim() || t('untitled');
		const slug = editSlug.trim();
		if (!/^[A-Za-z0-9._-]{1,128}$/.test(slug)) {
			editError = t('error.slug');
			return;
		}
		const access_code = editCode.trim();
		if (!/^[A-Za-z0-9._-]{1,128}$/.test(access_code)) {
			editError = t('error.accessCode');
			return;
		}
		if (!editContent) {
			editError = t('error.contentRequired');
			return;
		}
		if (new Blob([editContent]).size > maxFileSizeBytes) {
			editError = t('error.contentTooLarge', { size: maxFileSizeLabel });
			return;
		}
		editError = null;
		editBusy = true;
		const oldSlug = editFile.slug;
		try {
			const updated = await updateFile(oldSlug, {
				name,
				slug,
				html_content: editContent,
				access_code
			});
			files = files.map((f) => (f.slug === oldSlug ? updated : f));
			closeEdit();
		} catch (err) {
			// The only 409 this endpoint produces is a slug collision.
			if (err instanceof ApiError && err.status === 409) {
				editError = t('error.slugTaken');
			} else {
				editError = err instanceof Error ? err.message : t('error.save');
			}
		} finally {
			editBusy = false;
		}
	}

	function openExpiry(file: FileItem) {
		expiryFile = file;
		expiryNow = Date.now();
		if (file.expires_at) {
			// Only the deadline is stored, not the amount/unit it came from, so
			// the picker resets to the default rather than guessing.
			expiryMode = 'time';
			expiryTtlValue = 1;
			expiryTtlUnit = 'd';
		} else if (file.max_views != null) {
			expiryMode = 'views';
			expiryViews = file.max_views;
		} else {
			expiryMode = 'none';
		}
		expiryError = null;
		expiryOpen = true;
	}

	function closeExpiry() {
		expiryOpen = false;
		expiryFile = null;
	}

	async function saveExpiry() {
		if (!expiryFile) return;
		let payload: { ttl?: string | null; max_views?: number | null };
		if (expiryMode === 'time') {
			if (!ttlDeadline(expiryTtlValue, expiryTtlUnit, expiryNow)) {
				expiryError = t('error.ttlValue', { years: MAX_TTL_YEARS });
				return;
			}
			payload = { ttl: `${expiryTtlValue}${expiryTtlUnit}` };
		} else if (expiryMode === 'views') {
			if (!Number.isInteger(expiryViews) || expiryViews <= 0) {
				expiryError = t('error.viewCount');
				return;
			}
			payload = { max_views: expiryViews };
		} else {
			payload = {};
		}
		expiryError = null;
		expiryBusy = true;
		try {
			const updated = await setExpiry(expiryFile.slug, payload);
			files = files.map((f) => (f.slug === updated.slug ? updated : f));
			closeExpiry();
		} catch (err) {
			expiryError = err instanceof Error ? err.message : t('error.save');
		} finally {
			expiryBusy = false;
		}
	}

	async function onToggleVisibility(file: FileItem) {
		errorMessage = null;
		try {
			const updated = await setVisibility(file.slug, !file.is_public);
			files = files.map((f) => (f.slug === updated.slug ? updated : f));
		} catch (err) {
			errorMessage = err instanceof Error ? err.message : t('error.visibility');
		}
	}

	function startTitleEdit(file: FileItem) {
		titleEditSlug = file.slug;
		titleEditValue = file.name;
	}

	function cancelTitleEdit() {
		titleEditSlug = null;
		titleEditValue = '';
	}

	async function saveTitleEdit(file: FileItem) {
		const name = titleEditValue.trim();
		if (!name || name === file.name) {
			cancelTitleEdit();
			return;
		}
		errorMessage = null;
		try {
			const updated = await renameFile(file.slug, name);
			files = files.map((f) => (f.slug === updated.slug ? updated : f));
		} catch (err) {
			errorMessage = err instanceof Error ? err.message : t('error.rename');
		} finally {
			cancelTitleEdit();
		}
	}

	function startEditTags(file: FileItem) {
		tagInputSlug = file.slug;
		tagInputValue = fileTags(file).join(',');
	}

	function cancelEditTags() {
		tagInputSlug = null;
		tagInputValue = '';
	}

	// Mirrors the backend's normalizeTags (trim, drop empties, dedupe) so the
	// chips shown after save match what was typed; also accepts full-width
	// commas, which the backend doesn't split on.
	function normalizeTagsInput(value: string): string {
		return [
			...new Set(
				value
					.split(/[,，]/)
					.map((t) => t.trim())
					.filter(Boolean)
			)
		].join(',');
	}

	async function saveTags(file: FileItem) {
		const tags = normalizeTagsInput(tagInputValue);
		if (tags === fileTags(file).join(',')) {
			cancelEditTags();
			return;
		}
		errorMessage = null;
		try {
			const updated = await setTags(file.slug, tags);
			files = files.map((f) => (f.slug === updated.slug ? updated : f));
		} catch (err) {
			errorMessage = err instanceof Error ? err.message : t('error.saveTags');
		} finally {
			cancelEditTags();
		}
	}

	async function removeTag(file: FileItem, tag: string) {
		errorMessage = null;
		try {
			const updated = await setTags(
				file.slug,
				fileTags(file)
					.filter((t) => t !== tag)
					.join(',')
			);
			files = files.map((f) => (f.slug === updated.slug ? updated : f));
		} catch (err) {
			errorMessage = err instanceof Error ? err.message : t('error.removeTag');
		}
	}

	async function onRefreshCode(file: FileItem) {
		// Every link already shared for this file stops working the moment this
		// succeeds, and there is no undo -- the old code is gone.
		if (!confirm(t('confirm.refreshCode', { name: file.name }))) return;
		errorMessage = null;
		try {
			const updated = await refreshCode(file.slug);
			files = files.map((f) => (f.slug === updated.slug ? updated : f));
		} catch (err) {
			errorMessage = err instanceof Error ? err.message : t('error.refreshCode');
		}
	}

	async function onDelete(file: FileItem) {
		if (!confirm(t('confirm.delete', { name: file.name }))) return;
		errorMessage = null;
		try {
			await deleteFile(file.slug);
			files = files.filter((f) => f.slug !== file.slug);
			// Move it into the trash list too. Without this the tab kept whatever
			// it fetched the first time it was opened (trashedLoaded latches), so
			// a file deleted afterwards was missing from both lists and looked
			// like a soft delete had destroyed it.
			if (trashedLoaded) {
				trashedFiles = [file, ...trashedFiles];
			}
			await refreshUsage();
		} catch (err) {
			errorMessage = err instanceof Error ? err.message : t('error.delete');
		}
	}

	// Stored bytes changed, so the quota readout has to catch up. Failures are
	// ignored: a stale number is not worth an error banner over the action the
	// user actually asked for, and the next successful call corrects it.
	async function refreshUsage() {
		try {
			usage = await getUsage();
		} catch {
			/* keep the previous figure */
		}
	}

	async function onDeletePermanent(file: FileItem) {
		if (!confirm(t('confirm.deletePermanent', { name: file.name }))) return;
		errorMessage = null;
		try {
			await deleteFilePermanent(file.slug);
			trashedFiles = trashedFiles.filter((f) => f.slug !== file.slug);
			await refreshUsage();
		} catch (err) {
			errorMessage = err instanceof Error ? err.message : t('error.deletePermanent');
		}
	}

	async function onLogout() {
		await logout();
		await goto(resolve('/login'));
	}
</script>

<div
	class="min-h-screen bg-neutral-950 text-neutral-100"
	ondragenter={onDragEnter}
	ondragover={onDragOver}
	ondragleave={onDragLeave}
	ondrop={onDrop}
	role="presentation"
>
	{#if isDraggingOver}
		<div
			class="pointer-events-none fixed inset-0 z-20 flex items-center justify-center bg-neutral-950/80 backdrop-blur-sm"
		>
			<div
				class="flex flex-col items-center gap-3 rounded-2xl border-2 border-dashed border-emerald-500 bg-neutral-900 px-14 py-12"
			>
				<Icon icon="lucide:upload-cloud" width="32" height="32" class="text-emerald-400" />
				<p class="text-sm font-medium text-neutral-200">{t('upload.dropOverlay')}</p>
			</div>
		</div>
	{/if}

	<header
		class="sticky top-0 z-10 border-b border-neutral-800 bg-neutral-950/80 px-6 py-4 backdrop-blur sm:px-10"
	>
		<div class="mx-auto flex max-w-5xl items-center justify-between">
			<div class="flex items-center gap-2.5">
				<span
					class="flex h-8 w-8 items-center justify-center rounded-lg bg-emerald-500 text-neutral-950"
				>
					<Icon icon="lucide:file-code-2" width="18" height="18" />
				</span>
				<span class="text-base font-semibold tracking-tight">Renderbin</span>
			</div>

			<!-- Deliberately just three items. Account management and
			     backup/restore are super-admin-only sections of /settings rather
			     than top-bar buttons: they are rare, consequential operations, and
			     a bar that changes shape depending on who is signed in is worse
			     than one place to look for everything administrative. -->
			<div class="flex items-center gap-4">
				<!-- The signed-in account. On a multi-user instance there was
				     otherwise no way to tell which one you were in without
				     opening settings. Nickname, not username: it is the name the
				     user chose to be called, and the username is a login
				     credential with no reason to be on every screen. -->
				<!-- The layout guard redirects to /login before this page renders
				     without a user, so the guard here is for the type, not a case
				     that occurs. -->
				{#if data.user}
					<span
						class="hidden items-center gap-1.5 text-sm text-neutral-400 sm:flex"
						title={data.user.nickname}
					>
						<Icon icon="lucide:user" width="15" height="15" />
						<span class="max-w-32 truncate">{data.user.nickname}</span>
					</span>
				{/if}
				<LanguageSwitcher />
				<a
					href={resolve('/settings')}
					class="flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-sm text-neutral-400 transition-colors hover:bg-neutral-800 hover:text-neutral-100"
				>
					<Icon icon="lucide:settings" width="15" height="15" />
					{t('header.settings')}
				</a>
				<button
					onclick={onLogout}
					class="flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-sm text-neutral-400 transition-colors hover:bg-neutral-800 hover:text-neutral-100"
				>
					<Icon icon="lucide:log-out" width="15" height="15" />
					{t('header.logout')}
				</button>
			</div>
		</div>
	</header>

	<main class="mx-auto flex max-w-5xl flex-col gap-8 px-6 py-10 sm:px-10">
		<section class="flex flex-wrap items-end justify-between gap-4">
			<div>
				<p class="text-xs font-medium tracking-widest text-neutral-500 uppercase">
					{t('overview.title')}
				</p>
				<div class="mt-1 flex items-center gap-4">
					<button
						onclick={() => switchTab('files')}
						class={`text-2xl font-semibold tracking-tight transition-colors ${
							tab === 'files' ? 'text-neutral-100' : 'text-neutral-600 hover:text-neutral-400'
						}`}
					>
						{t('tab.files')}
					</button>
					<button
						onclick={() => switchTab('trashed')}
						class={`flex items-center gap-1.5 text-2xl font-semibold tracking-tight transition-colors ${
							tab === 'trashed' ? 'text-neutral-100' : 'text-neutral-600 hover:text-neutral-400'
						}`}
					>
						<Icon icon="lucide:trash-2" width="18" height="18" />
						{t('tab.trashed')}
					</button>
				</div>
				{#if tab === 'files'}
					<p class="mt-2 font-mono text-sm text-neutral-400">
						{t('count.total', { n: totalCount })} <span class="text-neutral-700">·</span>
						<span class="text-emerald-400">{t('count.public', { n: publicCount })}</span>
						<span class="text-neutral-700">·</span>
						<span class="text-neutral-500">{t('count.private', { n: privateCount })}</span>
					</p>
				{:else}
					<p class="mt-2 font-mono text-sm text-neutral-400">
						{t('count.inTrash', { n: trashedFiles.length })}
					</p>
				{/if}
				{#if usage}
					<p class="mt-1 font-mono text-xs text-neutral-600">
						{t('quota.used', {
							used: formatSize(usage.used_bytes),
							quota: formatSize(usage.quota_bytes)
						})}
					</p>
				{/if}
			</div>

			<div class="flex flex-col items-end gap-1.5">
				<div class="flex items-center gap-2">
					{#if tab === 'trashed'}
						<button
							onclick={onEmptyTrash}
							disabled={emptyingTrash || trashedFiles.length === 0}
							title={t('trash.emptyAllTitle')}
							class="flex items-center gap-2 rounded-lg bg-neutral-800 px-4 py-2 text-sm font-medium
								text-neutral-200 transition-colors hover:bg-red-500/10 hover:text-red-400
								disabled:cursor-not-allowed disabled:opacity-40"
						>
							<Icon
								icon={emptyingTrash ? 'lucide:loader-2' : 'lucide:trash-2'}
								width="16"
								height="16"
								class={emptyingTrash ? 'animate-spin' : ''}
							/>
							{t('trash.emptyAll')}
						</button>
					{/if}
					<button
						onclick={openCreate}
						class="flex items-center gap-2 rounded-lg bg-neutral-800 px-4 py-2 text-sm font-medium
							text-neutral-200 transition-colors hover:bg-neutral-700"
					>
						<Icon icon="lucide:file-plus-2" width="16" height="16" />
						{t('create.button')}
					</button>
					<button
						onclick={() => fileInput?.click()}
						disabled={uploading}
						class="flex items-center gap-2 rounded-lg bg-emerald-500 px-4 py-2 text-sm font-medium
							text-neutral-950 transition-colors hover:bg-emerald-400 disabled:opacity-50"
					>
						<Icon
							icon={uploading ? 'lucide:loader-2' : 'lucide:upload'}
							width="16"
							height="16"
							class={uploading ? 'animate-spin' : ''}
						/>
						{uploading ? t('upload.uploading') : t('upload.button')}
					</button>
					<input
						bind:this={fileInput}
						type="file"
						accept=".html,.htm,.md,.markdown,.txt,.text,text/html,text/markdown,text/plain"
						multiple
						class="hidden"
						onchange={onFileSelected}
					/>
				</div>
				<p class="text-xs text-neutral-600">{t('upload.hint')}</p>
			</div>
		</section>

		{#if errorMessage}
			<div
				class="flex items-center gap-2 rounded-lg border border-red-900/50 bg-red-500/10 px-3 py-2 text-sm text-red-400"
			>
				<Icon icon="lucide:alert-circle" width="16" height="16" class="shrink-0" />
				{errorMessage}
			</div>
		{/if}

		<!-- Shown when the browser refused both clipboard paths. The input is
		     auto-selected so the link is one Ctrl+C away; without it the URL
		     appears nowhere on the page and the user has to open the file just
		     to read it out of the address bar. -->
		{#if copyFallbackUrl}
			<div
				class="flex flex-wrap items-center gap-2 rounded-lg border border-amber-900/50 bg-amber-500/10 px-3 py-2 text-sm text-amber-300"
			>
				<Icon icon="lucide:clipboard-x" width="16" height="16" class="shrink-0" />
				<span>{t('copy.failed')}</span>
				<!-- svelte-ignore a11y_autofocus -->
				<input
					type="text"
					readonly
					autofocus
					value={copyFallbackUrl}
					onfocus={(e) => e.currentTarget.select()}
					class="min-w-0 flex-1 rounded border border-amber-900/50 bg-neutral-950 px-2 py-1 font-mono text-xs text-amber-200"
				/>
				<button
					onclick={() => (copyFallbackUrl = null)}
					aria-label={t('copy.dismiss')}
					title={t('copy.dismiss')}
					class="rounded p-1 text-amber-400/70 transition-colors hover:bg-amber-500/10 hover:text-amber-300"
				>
					<Icon icon="lucide:x" width="14" height="14" />
				</button>
			</div>
		{/if}

		<section class="overflow-hidden rounded-2xl border border-neutral-800 bg-neutral-900/40">
			{#if tab === 'files'}
				<!-- Compact single-row toolbar: search field + two segmented controls
				     (visibility, sort), all the same height. Wraps on narrow screens. -->
				<div class="flex flex-wrap items-center gap-2 border-b border-neutral-800 px-4 py-3">
					<div
						class="flex h-8 min-w-56 flex-1 items-stretch overflow-hidden rounded-lg border border-neutral-800 bg-neutral-950 transition-colors focus-within:border-emerald-500"
					>
						<div class="relative flex-1">
							<Icon
								icon="lucide:search"
								width="14"
								height="14"
								class="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-neutral-500"
							/>
							<input
								bind:value={search}
								oninput={scheduleSearch}
								placeholder={t('list.searchPlaceholder')}
								title={t('search.scopeTitle')}
								class="h-full w-full bg-transparent pr-8 pl-8 text-sm text-neutral-100 placeholder-neutral-600 outline-none"
							/>
							{#if searchLoading}
								<Icon
									icon="lucide:loader-2"
									width="14"
									height="14"
									class="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 animate-spin text-neutral-500"
								/>
							{/if}
						</div>
						<button
							onclick={toggleContentSearch}
							title={t('search.contentTitle')}
							aria-pressed={contentSearch}
							class={`flex shrink-0 items-center gap-1 border-l border-neutral-800 px-2.5 text-xs font-medium transition-colors ${
								contentSearch
									? 'bg-emerald-500/15 text-emerald-400'
									: 'text-neutral-500 hover:bg-neutral-800/60 hover:text-neutral-200'
							}`}
						>
							<Icon icon="lucide:file-search" width="13" height="13" />
							{t('search.content')}
						</button>
					</div>

					<div
						role="group"
						aria-label={t('list.visibility')}
						title={t('list.visibility')}
						class="flex h-8 shrink-0 items-center gap-0.5 rounded-lg border border-neutral-800 bg-neutral-950 p-0.5"
					>
						{#each visibilityOptions as opt (opt.key)}
							<button
								onclick={() => (visibilityFilter = opt.key)}
								class={`h-full rounded-md px-2.5 text-xs font-medium transition-colors ${
									visibilityFilter === opt.key
										? 'bg-emerald-500 text-neutral-950'
										: 'text-neutral-400 hover:bg-neutral-800 hover:text-neutral-200'
								}`}
							>
								{t(opt.labelKey)}
							</button>
						{/each}
					</div>

					<div
						role="group"
						aria-label={t('list.sort')}
						title={t('list.sort')}
						class="flex h-8 shrink-0 items-center gap-0.5 rounded-lg border border-neutral-800 bg-neutral-950 p-0.5"
					>
						<Icon
							icon="lucide:arrow-up-down"
							width="12"
							height="12"
							class="mx-1 shrink-0 text-neutral-600"
						/>
						{#each sortOptions as opt (opt.key)}
							<button
								onclick={() => onSortClick(opt)}
								class={`flex h-full items-center gap-1 rounded-md px-2.5 text-xs font-medium transition-colors ${
									sortKey === opt.key
										? 'bg-emerald-500 text-neutral-950'
										: 'text-neutral-400 hover:bg-neutral-800 hover:text-neutral-200'
								}`}
							>
								{t(opt.labelKey)}
								{#if sortKey === opt.key && opt.key !== 'recent'}
									<Icon
										icon={sortDir === 'asc' ? 'lucide:arrow-up' : 'lucide:arrow-down'}
										width="11"
										height="11"
									/>
								{/if}
							</button>
						{/each}
					</div>
				</div>
			{/if}

			{#snippet fileRow(file: FileItem, trashed: boolean, excerpt: string | null = null)}
				<li class="flex items-center gap-4 border-b border-neutral-800/60 px-4 py-3 last:border-0">
					<span
						class={`flex h-9 w-9 shrink-0 items-center justify-center rounded-lg ${
							file.is_public
								? 'bg-emerald-500/10 text-emerald-400'
								: 'bg-neutral-800 text-neutral-400'
						}`}
					>
						<Icon icon={kindIcon(file.kind)} width="17" height="17" />
					</span>

					<div class="min-w-0 flex-1">
						<div class="flex items-center gap-2">
							{#if trashed}
								<span class="truncate text-sm font-medium text-neutral-300">{file.name}</span>
							{:else if titleEditSlug === file.slug}
								<input
									bind:value={titleEditValue}
									use:autofocus
									onkeydown={(e) => {
										if (e.key === 'Enter') saveTitleEdit(file);
										if (e.key === 'Escape') cancelTitleEdit();
									}}
									class="min-w-0 flex-1 rounded-lg border border-neutral-700 bg-neutral-800 px-2 py-0.5 text-sm font-medium text-neutral-100 outline-none focus:border-emerald-500"
								/>
								<button
									onclick={() => saveTitleEdit(file)}
									title={t('common.save')}
									class="shrink-0 rounded p-0.5 text-emerald-400 transition-colors hover:bg-neutral-800"
								>
									<Icon icon="lucide:check" width="14" height="14" />
								</button>
								<button
									onclick={cancelTitleEdit}
									title={t('common.cancel')}
									class="shrink-0 rounded p-0.5 text-neutral-500 transition-colors hover:bg-neutral-800"
								>
									<Icon icon="lucide:x" width="14" height="14" />
								</button>
							{:else}
								<!-- eslint-disable svelte/no-navigation-without-resolve -- /res/[slug] is served by the Go backend, not a SvelteKit route -->
								<a
									href={resUrl(file)}
									target="_blank"
									data-sveltekit-reload
									class="truncate text-sm font-medium text-neutral-100 hover:text-emerald-400"
								>
									{file.name}
								</a>
								<!-- eslint-enable svelte/no-navigation-without-resolve -->
								<button
									onclick={() => startTitleEdit(file)}
									title={t('row.editTitle')}
									class="shrink-0 rounded p-0.5 text-neutral-600 transition-colors hover:bg-neutral-800 hover:text-neutral-200"
								>
									<Icon icon="lucide:pencil" width="12" height="12" />
								</button>
							{/if}
						</div>
						<div class="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-neutral-500">
							<span
								class="rounded bg-neutral-800 px-1.5 py-0.5 font-mono text-[10px] font-medium tracking-wide text-neutral-400"
								title={t('row.formatTitle', { kind: kindLabel(file.kind) })}
							>
								{kindLabel(file.kind)}
							</span>
							<span class="font-mono" title={t('row.sizeTitle')}>{formatSize(file.size)}</span>
							<span title={new Date(file.created_at).toLocaleString(intlLocale())}>
								{formatTime(file.created_at)}
							</span>
							{#if file.updated_at !== file.created_at}
								<span title={new Date(file.updated_at).toLocaleString(intlLocale())}>
									{t('row.updated', { time: formatTime(file.updated_at) })}
								</span>
							{/if}
							{#if file.expires_at}
								<span
									class="flex items-center gap-1 rounded bg-amber-500/10 px-1.5 py-0.5 text-amber-400"
									title={t('row.expiresTitle', {
										datetime: new Date(file.expires_at).toLocaleString(intlLocale())
									})}
								>
									<Icon icon="lucide:clock" width="12" height="12" />
									{t('row.expires', { datetime: formatDeadline(file.expires_at) })}
								</span>
							{/if}
							{#if file.max_views != null}
								<span
									class="flex items-center gap-1 rounded bg-amber-500/10 px-1.5 py-0.5 text-amber-400"
									title={t('row.viewsLeftTitle')}
								>
									<Icon icon="lucide:eye" width="12" height="12" />
									{t('row.viewsLeft', { n: Math.max(0, file.max_views - (file.view_count ?? 0)) })}
								</span>
							{/if}
							<!-- Why this link stopped working. Expiring clears the limit
							     columns, so without this the row could only say "private"
							     and the owner couldn't tell an expired link from one they
							     made private themselves. Shown only while the file is
							     still private: re-publishing it makes the note stale, and
							     setting a new limit clears it server-side. -->
							{#if !file.is_public && file.expired_reason}
								<span
									class="flex items-center gap-1 rounded bg-red-500/10 px-1.5 py-0.5 text-red-400"
									title={t('row.expiredTitle', {
										datetime: new Date(file.expired_at ?? '').toLocaleString(intlLocale())
									})}
								>
									<Icon icon="lucide:circle-slash" width="12" height="12" />
									{file.expired_reason === 'ttl' ? t('row.expiredTtl') : t('row.expiredViews')}
								</span>
							{/if}
							<span class="flex items-center gap-1 rounded bg-neutral-800 py-0.5 pr-1 pl-1.5">
								<span class="font-mono text-neutral-400">{file.access_code}</span>
								<button
									onclick={() => copyToClipboard(resUrl(file), file.slug)}
									title={t('row.copyLink')}
									class="text-neutral-500 transition-colors hover:text-neutral-200"
								>
									<Icon
										icon={copiedSlug === file.slug ? 'lucide:check' : 'lucide:copy'}
										width="12"
										height="12"
									/>
								</button>
							</span>
							<span
								class="flex items-center gap-1 text-emerald-400"
								title={t('row.successfulViews')}
							>
								<Icon icon="lucide:check-circle-2" width="12" height="12" />
								{file.success_count}
							</span>
							<span class="flex items-center gap-1 text-amber-400" title={t('row.codeViews')}>
								<Icon icon="lucide:key-round" width="12" height="12" />
								{file.code_success_count}
							</span>
							<span class="flex items-center gap-1 text-red-400" title={t('row.failedViews')}>
								<Icon icon="lucide:x-circle" width="12" height="12" />
								{file.failure_count}
							</span>
						</div>
						{#if excerpt}
							<!-- Content-search hit: the matched passage, deliberately de-emphasized. -->
							<p class="mt-1 line-clamp-2 text-xs break-all text-neutral-500">{excerpt}</p>
						{/if}
						<div class="mt-1.5 flex flex-wrap items-center gap-1.5">
							{#if !trashed && tagInputSlug === file.slug}
								<input
									bind:value={tagInputValue}
									use:autofocus
									placeholder={t('row.tagsPlaceholder')}
									onkeydown={(e) => {
										if (e.key === 'Enter') saveTags(file);
										if (e.key === 'Escape') cancelEditTags();
									}}
									class="w-64 rounded-full border border-neutral-700 bg-neutral-800 px-2 py-0.5 text-xs text-neutral-100 outline-none focus:border-emerald-500"
								/>
								<button
									onclick={() => saveTags(file)}
									title={t('common.save')}
									class="rounded p-0.5 text-emerald-400 transition-colors hover:bg-neutral-800"
								>
									<Icon icon="lucide:check" width="12" height="12" />
								</button>
								<button
									onclick={cancelEditTags}
									title={t('common.cancel')}
									class="rounded p-0.5 text-neutral-500 transition-colors hover:bg-neutral-800"
								>
									<Icon icon="lucide:x" width="12" height="12" />
								</button>
							{:else}
								{#each fileTags(file) as tag (tag)}
									<span
										class="flex items-center gap-1 rounded-full bg-neutral-800 py-0.5 pr-1 pl-2 text-xs text-neutral-300"
									>
										{tag}
										{#if !trashed}
											<button
												onclick={() => removeTag(file, tag)}
												title={t('row.removeTag', { tag })}
												class="rounded p-0.5 text-neutral-500 transition-colors hover:text-red-400"
											>
												<Icon icon="lucide:x" width="10" height="10" />
											</button>
										{/if}
									</span>
								{/each}
								{#if !trashed}
									<button
										onclick={() => startEditTags(file)}
										title={t('row.editTags')}
										class="flex items-center gap-0.5 rounded-full border border-dashed border-neutral-700 px-2 py-0.5 text-xs text-neutral-500 transition-colors hover:border-neutral-500 hover:text-neutral-300"
									>
										<Icon
											icon={fileTags(file).length > 0 ? 'lucide:pencil' : 'lucide:plus'}
											width="10"
											height="10"
										/>
										{t('row.tag')}
									</button>
								{/if}
							{/if}
						</div>
					</div>

					{#if trashed}
						<div class="flex shrink-0 items-center gap-1.5">
							<button
								onclick={() => onRestore(file)}
								class="flex items-center gap-1.5 rounded-lg bg-neutral-800 px-3 py-1.5 text-xs font-medium text-neutral-200 transition-colors hover:bg-neutral-700"
							>
								<Icon icon="lucide:rotate-ccw" width="14" height="14" />
								{t('row.restore')}
							</button>
							<button
								onclick={() => onDeletePermanent(file)}
								class="flex items-center gap-1.5 rounded-lg bg-neutral-800 px-3 py-1.5 text-xs font-medium text-neutral-200 transition-colors hover:bg-red-500/10 hover:text-red-400"
							>
								<Icon icon="lucide:trash-2" width="14" height="14" />
								{t('row.deletePermanent')}
							</button>
						</div>
					{:else}
						<div class="flex shrink-0 items-center gap-1.5">
							<span class={`text-xs ${file.is_public ? 'text-emerald-400' : 'text-neutral-500'}`}>
								{file.is_public ? t('row.public') : t('row.private')}
							</span>
							<button
								role="switch"
								aria-checked={file.is_public}
								aria-label={t('row.toggleVisibility', { name: file.name })}
								onclick={() => onToggleVisibility(file)}
								class={`relative h-5 w-9 rounded-full transition-colors ${
									file.is_public ? 'bg-emerald-500' : 'bg-neutral-700'
								}`}
							>
								<span
									class={`absolute top-0.5 left-0.5 h-4 w-4 rounded-full bg-white transition-transform ${
										file.is_public ? 'translate-x-4' : 'translate-x-0'
									}`}
								></span>
							</button>

							<!-- eslint-disable svelte/no-navigation-without-resolve -- /api/files/[slug]/download is served by the Go backend, not a SvelteKit route -->
							<a
								href={`/api/files/${file.slug}/download`}
								download={downloadName(file)}
								title={t('row.downloadSource')}
								class="rounded-lg p-2 text-neutral-500 transition-colors hover:bg-neutral-800 hover:text-neutral-200"
							>
								<Icon icon="lucide:download" width="15" height="15" />
							</a>
							<!-- eslint-enable svelte/no-navigation-without-resolve -->
							<button
								onclick={() => openEdit(file)}
								title={t('row.edit')}
								class="rounded-lg p-2 text-neutral-500 transition-colors hover:bg-neutral-800 hover:text-neutral-200"
							>
								<Icon icon="lucide:file-pen" width="15" height="15" />
							</button>
							<button
								onclick={() => openExpiry(file)}
								title={t('row.setExpiry')}
								class="rounded-lg p-2 text-neutral-500 transition-colors hover:bg-neutral-800 hover:text-neutral-200"
							>
								<Icon icon="lucide:clock" width="15" height="15" />
							</button>
							<button
								onclick={() => onRefreshCode(file)}
								title={t('row.refreshCode')}
								class="rounded-lg p-2 text-neutral-500 transition-colors hover:bg-neutral-800 hover:text-neutral-200"
							>
								<Icon icon="lucide:refresh-cw" width="15" height="15" />
							</button>
							<button
								onclick={() => onDelete(file)}
								title={t('row.delete')}
								class="rounded-lg p-2 text-neutral-500 transition-colors hover:bg-red-500/10 hover:text-red-400"
							>
								<Icon icon="lucide:trash-2" width="15" height="15" />
							</button>
						</div>
					{/if}
				</li>
			{/snippet}

			{#if tab === 'trashed'}
				{#if trashLoading}
					<div class="flex flex-col items-center gap-3 py-16 text-center">
						<Icon
							icon="lucide:loader-2"
							width="24"
							height="24"
							class="animate-spin text-neutral-600"
						/>
						<p class="text-sm text-neutral-500">{t('trash.loading')}</p>
					</div>
				{:else if trashedFiles.length === 0}
					<div class="flex flex-col items-center gap-3 py-16 text-center">
						<Icon icon="lucide:trash-2" width="28" height="28" class="text-neutral-700" />
						<p class="text-sm text-neutral-500">{t('trash.empty')}</p>
					</div>
				{:else}
					<ul>
						{#each trashedFiles as file (file.slug)}
							{@render fileRow(file, true)}
						{/each}
					</ul>
				{/if}
			{:else if searchActive}
				{#if searchLoading && searchResults === null}
					<div class="flex flex-col items-center gap-3 py-16 text-center">
						<Icon
							icon="lucide:loader-2"
							width="24"
							height="24"
							class="animate-spin text-neutral-600"
						/>
						<p class="text-sm text-neutral-500">{t('search.loading')}</p>
					</div>
				{:else if searchRows.length === 0}
					<div class="flex flex-col items-center gap-3 py-16 text-center">
						<Icon icon="lucide:search-x" width="28" height="28" class="text-neutral-700" />
						<p class="text-sm text-neutral-500">{t('search.empty')}</p>
					</div>
				{:else}
					<ul>
						{#each searchRows as row (row.file.slug)}
							{@render fileRow(row.file, false, row.snippet)}
						{/each}
					</ul>
				{/if}
			{:else if filteredFiles.length === 0}
				<div class="flex flex-col items-center gap-3 py-16 text-center">
					<Icon icon="lucide:inbox" width="28" height="28" class="text-neutral-700" />
					<p class="text-sm text-neutral-500">
						{files.length === 0 ? t('files.emptyNone') : t('files.emptyFiltered')}
					</p>
				</div>
			{:else if sortKey === 'recent'}
				{#each groups as group (group.label)}
					<div class="px-4 pt-4 text-xs font-medium tracking-widest text-neutral-500 uppercase">
						{group.label}
					</div>
					<ul>
						{#each group.items as file (file.slug)}
							{@render fileRow(file, false)}
						{/each}
					</ul>
				{/each}
			{:else}
				<ul>
					{#each sortedFlat as file (file.slug)}
						{@render fileRow(file, false)}
					{/each}
				</ul>
			{/if}
		</section>
	</main>
</div>

<svelte:window
	onkeydown={(e) => {
		if (e.key === 'Escape') {
			if (editOpen) closeEdit();
			if (expiryOpen) closeExpiry();
			if (createOpen && !createBusy) closeCreate();
		}
	}}
/>

{#if createOpen}
	<div
		class="fixed inset-0 z-30 flex items-center justify-center bg-neutral-950/70 p-4 backdrop-blur-sm"
		onclick={() => !createBusy && closeCreate()}
		role="presentation"
	>
		<div
			class="flex max-h-[90vh] w-full max-w-2xl flex-col gap-4 rounded-2xl border border-neutral-800 bg-neutral-900 p-6 shadow-2xl"
			onclick={(e) => e.stopPropagation()}
			role="presentation"
		>
			<div class="flex items-center justify-between">
				<h2 class="text-lg font-semibold tracking-tight">{t('create.title')}</h2>
				<button
					onclick={closeCreate}
					disabled={createBusy}
					class="rounded-lg p-1 text-neutral-500 transition-colors hover:bg-neutral-800 hover:text-neutral-200"
				>
					<Icon icon="lucide:x" width="18" height="18" />
				</button>
			</div>

			<label class="flex flex-col gap-1 text-xs font-medium text-neutral-400">
				{t('create.nameLabel')}
				<input
					bind:value={createName}
					use:autofocus
					placeholder={t('untitled')}
					class="rounded-lg border border-neutral-800 bg-neutral-950 px-3 py-2 text-sm text-neutral-100 placeholder-neutral-600 outline-none focus:border-emerald-500"
				/>
			</label>

			<div class="flex flex-col gap-1">
				<span class="text-xs font-medium text-neutral-400">{t('create.kindLabel')}</span>
				<div
					role="group"
					aria-label={t('create.kindLabel')}
					class="flex w-fit items-center gap-0.5 rounded-lg border border-neutral-800 bg-neutral-950 p-0.5"
				>
					{#each kindOptions as opt (opt.key)}
						<button
							onclick={() => (createKind = opt.key)}
							aria-pressed={createKind === opt.key}
							class={`flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs font-medium transition-colors ${
								createKind === opt.key
									? 'bg-emerald-500 text-neutral-950'
									: 'text-neutral-400 hover:bg-neutral-800 hover:text-neutral-200'
							}`}
						>
							<Icon icon={kindIcon(opt.key)} width="13" height="13" />
							{t(opt.labelKey)}
						</button>
					{/each}
				</div>
				<p class="text-xs text-neutral-600">{t('create.kindHint')}</p>
			</div>

			<label class="flex min-h-0 flex-col gap-1 text-xs font-medium text-neutral-400">
				{t('create.contentLabel')}
				<textarea
					bind:value={createContent}
					spellcheck="false"
					placeholder={t('create.contentPlaceholder')}
					class="min-h-[240px] flex-1 resize-none rounded-lg border border-neutral-800 bg-neutral-950 px-3 py-2 font-mono text-xs text-neutral-100 placeholder-neutral-600 outline-none focus:border-emerald-500"
				></textarea>
			</label>

			{#if createError}
				<div
					role="alert"
					class="flex items-center gap-2 rounded-lg border border-red-900/50 bg-red-500/10 px-3 py-2 text-sm text-red-400"
				>
					<Icon icon="lucide:alert-circle" width="16" height="16" class="shrink-0" />
					{createError}
				</div>
			{/if}

			<div class="flex justify-end gap-2">
				<button
					onclick={closeCreate}
					disabled={createBusy}
					class="rounded-lg px-4 py-2 text-sm font-medium text-neutral-400 transition-colors hover:bg-neutral-800 hover:text-neutral-100 disabled:opacity-50"
				>
					{t('common.cancel')}
				</button>
				<button
					onclick={submitCreate}
					disabled={createBusy}
					class="flex items-center gap-2 rounded-lg bg-emerald-500 px-4 py-2 text-sm font-medium text-neutral-950 transition-colors hover:bg-emerald-400 disabled:opacity-50"
				>
					{#if createBusy}
						<Icon icon="lucide:loader-2" width="15" height="15" class="animate-spin" />
					{/if}
					{createBusy ? t('create.creating') : t('create.submit')}
				</button>
			</div>
		</div>
	</div>
{/if}

{#if editOpen}
	<div
		class="fixed inset-0 z-30 flex items-center justify-center bg-neutral-950/70 p-4 backdrop-blur-sm"
		onclick={closeEdit}
		role="presentation"
	>
		<div
			class="flex max-h-[90vh] w-full max-w-2xl flex-col gap-4 rounded-2xl border border-neutral-800 bg-neutral-900 p-6 shadow-2xl"
			onclick={(e) => e.stopPropagation()}
			role="presentation"
		>
			<div class="flex items-center justify-between">
				<h2 class="text-lg font-semibold tracking-tight">{t('edit.title')}</h2>
				<button
					onclick={closeEdit}
					class="rounded-lg p-1 text-neutral-500 transition-colors hover:bg-neutral-800 hover:text-neutral-200"
				>
					<Icon icon="lucide:x" width="18" height="18" />
				</button>
			</div>

			<label class="flex flex-col gap-1 text-xs font-medium text-neutral-400">
				{t('edit.titleLabel')}
				<input
					bind:value={editTitle}
					class="rounded-lg border border-neutral-800 bg-neutral-950 px-3 py-2 text-sm text-neutral-100 outline-none focus:border-emerald-500"
				/>
			</label>

			<label class="flex flex-col gap-1 text-xs font-medium text-neutral-400">
				{t('edit.slugLabel')}
				<input
					bind:value={editSlug}
					spellcheck="false"
					class="rounded-lg border border-neutral-800 bg-neutral-950 px-3 py-2 font-mono text-sm text-neutral-100 outline-none focus:border-emerald-500"
				/>
			</label>

			<label class="flex flex-col gap-1 text-xs font-medium text-neutral-400">
				{t('edit.codeLabel')}
				<input
					bind:value={editCode}
					spellcheck="false"
					class="rounded-lg border border-neutral-800 bg-neutral-950 px-3 py-2 font-mono text-sm text-neutral-100 outline-none focus:border-emerald-500"
				/>
			</label>

			<div class="flex min-h-0 flex-col gap-1">
				<button
					onclick={() => (editContentOpen = !editContentOpen)}
					title={t('edit.toggleContent')}
					aria-expanded={editContentOpen}
					class="flex items-center gap-1 self-start text-xs font-medium text-neutral-400 transition-colors hover:text-neutral-200"
				>
					<Icon
						icon={editContentOpen ? 'lucide:chevron-down' : 'lucide:chevron-right'}
						width="14"
						height="14"
					/>
					{sourceLabel(editFile?.kind)}
				</button>
				{#if editContentOpen}
					<textarea
						bind:value={editContent}
						disabled={editBusy}
						spellcheck="false"
						class="min-h-[240px] flex-1 resize-none rounded-lg border border-neutral-800 bg-neutral-950 px-3 py-2 font-mono text-xs text-neutral-100 outline-none focus:border-emerald-500 disabled:opacity-50"
					></textarea>
				{/if}
			</div>

			{#if editError}
				<div
					role="alert"
					class="flex items-center gap-2 rounded-lg border border-red-900/50 bg-red-500/10 px-3 py-2 text-sm text-red-400"
				>
					<Icon icon="lucide:alert-circle" width="16" height="16" class="shrink-0" />
					{editError}
				</div>
			{/if}

			<div class="flex justify-end gap-2">
				<button
					onclick={closeEdit}
					class="rounded-lg px-4 py-2 text-sm font-medium text-neutral-400 transition-colors hover:bg-neutral-800 hover:text-neutral-100"
				>
					{t('common.cancel')}
				</button>
				<button
					onclick={saveEdit}
					disabled={editBusy}
					class="flex items-center gap-2 rounded-lg bg-emerald-500 px-4 py-2 text-sm font-medium text-neutral-950 transition-colors hover:bg-emerald-400 disabled:opacity-50"
				>
					{#if editBusy}
						<Icon icon="lucide:loader-2" width="15" height="15" class="animate-spin" />
					{/if}
					{editBusy ? t('common.saving') : t('common.save')}
				</button>
			</div>
		</div>
	</div>
{/if}

{#if expiryOpen}
	<div
		class="fixed inset-0 z-30 flex items-center justify-center bg-neutral-950/70 p-4 backdrop-blur-sm"
		onclick={closeExpiry}
		role="presentation"
	>
		<div
			class="flex w-full max-w-md flex-col gap-4 rounded-2xl border border-neutral-800 bg-neutral-900 p-6 shadow-2xl"
			onclick={(e) => e.stopPropagation()}
			role="presentation"
		>
			<div class="flex items-center justify-between">
				<h2 class="text-lg font-semibold tracking-tight">{t('expiry.title')}</h2>
				<button
					onclick={closeExpiry}
					class="rounded-lg p-1 text-neutral-500 transition-colors hover:bg-neutral-800 hover:text-neutral-200"
				>
					<Icon icon="lucide:x" width="18" height="18" />
				</button>
			</div>

			<p class="text-xs text-neutral-500">{t('expiry.note')}</p>

			<div class="flex items-center gap-1.5">
				{#each [{ key: 'none', labelKey: 'expiry.unlimited' }, { key: 'time', labelKey: 'expiry.timeLimit' }, { key: 'views', labelKey: 'expiry.viewLimit' }] as const as opt (opt.key)}
					<button
						onclick={() => (expiryMode = opt.key)}
						class={`rounded-full px-3 py-1 text-xs font-medium transition-colors ${
							expiryMode === opt.key
								? 'bg-emerald-500 text-neutral-950'
								: 'bg-neutral-800 text-neutral-400 hover:bg-neutral-700 hover:text-neutral-200'
						}`}
					>
						{t(opt.labelKey)}
					</button>
				{/each}
			</div>

			{#if expiryMode === 'time'}
				<div class="flex flex-col gap-1.5">
					<span class="text-xs font-medium text-neutral-400">{t('expiry.expiresAfter')}</span>
					<div class="flex flex-wrap items-center gap-1.5">
						<input
							type="number"
							min="1"
							step="1"
							bind:value={expiryTtlValue}
							class="w-20 rounded-lg border border-neutral-800 bg-neutral-950 px-3 py-2 text-sm text-neutral-100 outline-none focus:border-emerald-500"
						/>
						<div class="flex flex-wrap items-center gap-1.5">
							{#each ttlUnits as unit (unit.key)}
								<button
									onclick={() => (expiryTtlUnit = unit.key)}
									class={`rounded-lg px-3 py-1.5 text-xs font-medium transition-colors ${
										expiryTtlUnit === unit.key
											? 'bg-neutral-100 text-neutral-950'
											: 'bg-neutral-800 text-neutral-300 hover:bg-neutral-700'
									}`}
								>
									{t(unit.labelKey)}
								</button>
							{/each}
						</div>
					</div>
					<!-- The deadline in plain words: "1 month" is easy to pick and hard
					     to picture, and this is the number the owner has to plan around. -->
					<p class="text-xs text-neutral-500">
						{#if expiryPreview}
							{t('expiry.preview', { datetime: expiryPreview.toLocaleString(intlLocale()) })}
						{:else}
							{t('error.ttlValue', { years: MAX_TTL_YEARS })}
						{/if}
					</p>
				</div>
			{:else if expiryMode === 'views'}
				<label class="flex flex-col gap-1.5 text-xs font-medium text-neutral-400">
					{t('expiry.maxViews')}
					<input
						type="number"
						min="1"
						bind:value={expiryViews}
						class="w-32 rounded-lg border border-neutral-800 bg-neutral-950 px-3 py-2 text-sm text-neutral-100 outline-none focus:border-emerald-500"
					/>
				</label>
			{:else}
				<p class="text-xs text-neutral-600">{t('expiry.noExpiry')}</p>
			{/if}

			{#if expiryError}
				<p class="text-sm text-red-400">{expiryError}</p>
			{/if}

			<div class="flex justify-end gap-2">
				<button
					onclick={closeExpiry}
					class="rounded-lg px-4 py-2 text-sm font-medium text-neutral-400 transition-colors hover:bg-neutral-800 hover:text-neutral-100"
				>
					{t('common.cancel')}
				</button>
				<button
					onclick={saveExpiry}
					disabled={expiryBusy}
					class="flex items-center gap-2 rounded-lg bg-emerald-500 px-4 py-2 text-sm font-medium text-neutral-950 transition-colors hover:bg-emerald-400 disabled:opacity-50"
				>
					{#if expiryBusy}
						<Icon icon="lucide:loader-2" width="15" height="15" class="animate-spin" />
					{/if}
					{expiryBusy ? t('common.saving') : t('common.save')}
				</button>
			</div>
		</div>
	</div>
{/if}
