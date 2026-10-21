<script lang="ts">
	import Icon from '$lib/components/Icon.svelte';
	import Alert from '$lib/components/Alert.svelte';
	import Shell from '$lib/components/Shell.svelte';
	import Spinner from '$lib/components/Spinner.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import type { MessageKey } from '$lib/i18n/messages';
	import { copyFeedback } from '$lib/copy.svelte';
	import { formatSize } from '$lib/format';
	import { dayLabel } from '$lib/dates';
	import { errText } from '$lib/errors';
	import { ApiError } from '$lib/api/http';
	import { getUsage, type Usage } from '$lib/api/settings';
	import { pickedFromEntries, pickedFromInput, type PickedFile } from '$lib/site';
	import {
		deleteFile,
		deleteFilePermanent,
		emptyTrash,
		listFilesPage,
		refreshCode,
		renameFile,
		restoreFile,
		setTags,
		setVisibility,
		uploadSite,
		type FileCounts,
		type FileItem,
		type PageQuery,
		type SearchResult,
		type SortKey
	} from '$lib/api/files';
	import { resUrl, zipFolder, zipTooLarge } from '$lib/files-ui';
	import { planUpload, uploadDocument } from '$lib/upload';
	import FileRow from './FileRow.svelte';
	import CreateModal from './CreateModal.svelte';
	import EditModal from './EditModal.svelte';
	import ExpiryModal from './ExpiryModal.svelte';
	import SearchBar from './SearchBar.svelte';
	import StorageMeter from './StorageMeter.svelte';

	let { data } = $props();

	// The loaded rows of the current view, in the server's order, a page at a
	// time (see listFilesPage), and how many files each view holds. Snapshots of
	// the first page on purpose: they are mutated locally for optimistic updates
	// and must not re-sync when `data` changes.
	// svelte-ignore state_referenced_locally
	let rows = $state<FileItem[]>(data.page.files);
	// svelte-ignore state_referenced_locally
	let nextCursor = $state(data.page.next_cursor);
	// svelte-ignore state_referenced_locally
	let counts = $state<FileCounts>(data.page.counts);
	let listLoading = $state(false);
	let loadingMore = $state(false);
	// Each (re)load invalidates the ones before it, so a slow answer for a view
	// the user has already left never lands.
	let listSeq = 0;
	// Storage use against the account's quota. Loaded with the list, but counts
	// trashed files too, which is what the server enforces on upload; summing the
	// visible rows would show a smaller number than the one that rejects the next
	// upload.
	// svelte-ignore state_referenced_locally
	let usage = $state<Usage | null>(data.usage);

	let uploading = $state(false);
	let errorMessage = $state<string | null>(null);
	let fileInput = $state<HTMLInputElement | null>(null);
	let folderInput = $state<HTMLInputElement | null>(null);
	let isDraggingOver = $state(false);
	let dragCounter = 0;

	// The sidebar (or, on phones, the chip row) picks one view: a visibility
	// filter over the live files, or the trash. Each is a listing of its own on
	// the server, fetched page by page.
	type View = 'all' | 'public' | 'private' | 'trash';
	let view = $state<View>('all');
	const inTrash = $derived(view === 'trash');
	let emptyingTrash = $state(false);

	// Which modal is open. Each owns its own form state and reports back through
	// callbacks; the page only swaps rows in `files`.
	let creating = $state(false);
	let editing = $state<FileItem | null>(null);
	let expiring = $state<FileItem | null>(null);

	// A non-empty query swaps the list for the (flat) result view.
	let search = $state('');
	let searchResults = $state<SearchResult[] | null>(null);
	let searchLoading = $state(false);
	const searchActive = $derived(search.trim().length > 0);

	const copied = copyFeedback();
	// The URL a copy attempt failed on. Both clipboard paths can be refused
	// (an old browser on a plain-HTTP LAN, or a denied permission), and a share
	// link the user cannot reach any other way is worse than an ugly one they
	// can -- so on failure the page renders it as selectable text instead.
	let copyFallbackUrl = $state<string | null>(null);

	const sortOptions: { key: SortKey; labelKey: MessageKey; defaultDir: 'asc' | 'desc' }[] = [
		{ key: 'recent', labelKey: 'sort.recent', defaultDir: 'desc' },
		{ key: 'name', labelKey: 'sort.name', defaultDir: 'asc' },
		{ key: 'success', labelKey: 'sort.success', defaultDir: 'desc' },
		{ key: 'failure', labelKey: 'sort.failure', defaultDir: 'desc' }
	];
	let sortKey = $state<SortKey>('recent');
	let sortDir = $state<'asc' | 'desc'>('desc');

	function onSortChange(key: SortKey) {
		sortKey = key;
		sortDir = sortOptions.find((o) => o.key === key)!.defaultDir;
		reload();
	}

	function toggleSortDir() {
		sortDir = sortDir === 'asc' ? 'desc' : 'asc';
		reload();
	}

	const navItems = $derived<{ key: View; icon: string; label: string; count: number }[]>([
		{ key: 'all', icon: 'lucide:layout-list', label: t('nav.all'), count: counts.all },
		{ key: 'public', icon: 'lucide:globe', label: t('visibility.public'), count: counts.public },
		{ key: 'private', icon: 'lucide:lock', label: t('visibility.private'), count: counts.private },
		{ key: 'trash', icon: 'lucide:trash-2', label: t('tab.trashed'), count: counts.trash }
	]);
	const viewTitle = $derived(navItems.find((i) => i.key === view)!.label);

	function pageQuery(cursor?: string): PageQuery {
		return view === 'trash'
			? { trash: true, cursor }
			: { sort: sortKey, dir: sortDir, visibility: view, cursor };
	}

	// Replaces the list with the first page of the current view and order.
	async function reload() {
		const mine = ++listSeq;
		let loaded = false;
		listLoading = true;
		try {
			const page = await listFilesPage(pageQuery());
			if (mine !== listSeq) return;
			rows = page.files;
			nextCursor = page.next_cursor;
			counts = page.counts;
			loaded = true;
		} catch (err) {
			if (mine === listSeq)
				errorMessage = errText(err, inTrash ? 'error.loadTrash' : 'error.loadFiles');
		} finally {
			if (mine === listSeq) listLoading = false;
		}
		if (loaded && endVisible) loadMore();
	}

	// Appends the next page. Rows already on screen are skipped: a file added
	// locally (an upload) can also arrive in a later page.
	async function loadMore() {
		if (!nextCursor || loadingMore || listLoading) return;
		const mine = listSeq;
		let loaded = false;
		loadingMore = true;
		try {
			const page = await listFilesPage(pageQuery(nextCursor));
			if (mine !== listSeq) return;
			const have = new Set(rows.map((f) => f.slug));
			rows = [...rows, ...page.files.filter((f) => !have.has(f.slug))];
			nextCursor = page.next_cursor;
			counts = page.counts;
			loaded = true;
		} catch (err) {
			if (mine === listSeq) errorMessage = errText(err, 'error.loadFiles');
		} finally {
			loadingMore = false;
		}
		// A short page can leave the end of the list on screen, and the
		// observer only fires on a change. Only after a success: a failing
		// request must not retry in a loop; the button is there for that.
		if (loaded && endVisible) loadMore();
	}

	// Loads the next page as the end of the list nears the viewport (600px
	// early, so a steady scroll rarely waits for it). The button there does the
	// same for keyboards and for browsers without IntersectionObserver.
	let endVisible = false;
	function nearEnd(node: HTMLElement) {
		if (typeof IntersectionObserver === 'undefined') return;
		const io = new IntersectionObserver(
			(entries) => {
				endVisible = entries.some((e) => e.isIntersecting);
				if (endVisible) loadMore();
			},
			{ rootMargin: '600px' }
		);
		io.observe(node);
		return () => {
			endVisible = false;
			io.disconnect();
		};
	}

	function selectView(next: View) {
		if (next === view) return;
		view = next;
		reload();
	}

	function matchesView(f: FileItem): boolean {
		if (view === 'public') return f.is_public;
		if (view === 'private') return !f.is_public;
		return view === 'all';
	}

	// Grouped-by-day view, used only for the default "Recent" sort. The rows
	// arrive newest first, so grouping what has loaded so far is the same as
	// grouping everything, up to the last day shown.
	const groups = $derived.by(() => {
		const order: string[] = [];
		// eslint-disable-next-line svelte/prefer-svelte-reactivity -- a local, rebuilt on every derivation
		const byLabel = new Map<string, FileItem[]>();
		for (const file of rows) {
			const label = dayLabel(file.created_at);
			if (!byLabel.has(label)) {
				byLabel.set(label, []);
				order.push(label);
			}
			byLabel.get(label)!.push(file);
		}
		return order.map((label) => ({ label, items: byLabel.get(label)! }));
	});

	// SQLite's NOCASE, which is how the server sorts names: ASCII folded, then
	// code points. Search results arrive as one complete set and are sorted
	// here, the same way, so the two lists never disagree.
	function nocase(s: string): string {
		return s.replace(/[A-Z]/g, (c) => c.toLowerCase());
	}

	function compareFiles(a: FileItem, b: FileItem): number {
		const dir = sortDir === 'asc' ? 1 : -1;
		if (sortKey === 'name') {
			const x = nocase(a.name);
			const y = nocase(b.name);
			return dir * (x < y ? -1 : x > y ? 1 : 0);
		}
		if (sortKey === 'success') return dir * (a.success_count - b.success_count);
		if (sortKey === 'failure') return dir * (a.failure_count - b.failure_count);
		return 0; // Recent: the server already answers newest first
	}

	// Search results are full rows of their own (the list may not have loaded
	// a match yet), kept in step with local mutations by replaceRow/dropRow. A
	// title match renders as a plain row; any other carries the weakened
	// snippet. Always flat (never day-grouped).
	const searchRows = $derived.by(() => {
		if (!searchResults) return [];
		return searchResults
			.filter(matchesView)
			.map((r) => ({ file: r as FileItem, snippet: r.matched_name ? null : r.snippet || null }))
			.sort((a, b) => compareFiles(a.file, b.file));
	});

	async function copyLink(file: FileItem) {
		copyFallbackUrl = null;
		const text = resUrl(file);
		if (!(await copied.copy(text, file.slug))) copyFallbackUrl = text;
	}

	// New files land in the live list. In "Recent" they go on top, which is
	// known without asking; any other order, or the trash, is reloaded.
	function showCreated(created: FileItem[]) {
		for (const f of created) countLive(f, 1);
		if (view === 'trash') {
			view = 'all';
			reload();
		} else if (sortKey === 'recent') {
			rows = [...created.filter(matchesView), ...rows];
		} else {
			reload();
		}
	}

	// Uploads one folder as a site. Named after its index.html <title> by the
	// server; editable inline afterwards like any file.
	async function uploadFolder(picked: PickedFile[]) {
		if (uploading) return;
		errorMessage = null;
		uploading = true;
		try {
			const zip = await zipFolder(picked);
			if (typeof zip === 'string') {
				errorMessage = zip;
				return;
			}
			showCreated([await uploadSite(null, zip)]);
			await refreshUsage();
		} catch (err) {
			errorMessage = errText(err, 'note.failed', { n: 1 });
		} finally {
			uploading = false;
		}
	}

	async function onFolderSelected(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		if (!input.files || input.files.length === 0) return;
		await uploadFolder(pickedFromInput(input.files));
		input.value = '';
	}

	// Shared by the file picker (single or multi-select) and drag-and-drop
	// (single or multi-file).
	async function uploadMany(fileList: FileList | File[]) {
		if (uploading) return;

		const plan = planUpload(Array.from(fileList));
		if (plan.documents.length === 0 && plan.zips.length === 0) {
			errorMessage =
				plan.oversize > 0 ? t('error.oversizeOnly', { n: plan.oversize }) : t('error.noSupported');
			return;
		}

		errorMessage = null;
		uploading = true;
		const created: FileItem[] = [];
		const notes: string[] = [];
		let failedCount = 0;
		try {
			for (const { file, kind } of plan.documents) {
				try {
					created.push(await uploadDocument(file, kind));
				} catch {
					failedCount++;
				}
			}
			// Site failures are reported by name with the server's reason
			// ("needs an index.html", "not UTF-8", ...): a bare count would leave
			// the user guessing what to fix in their zip.
			for (const zip of plan.zips) {
				const tooLarge = zipTooLarge(zip);
				if (tooLarge) {
					notes.push(tooLarge);
					continue;
				}
				try {
					created.push(await uploadSite(null, zip));
				} catch (err) {
					notes.push(
						t('note.siteFailed', {
							name: zip.name,
							reason: err instanceof ApiError ? err.message : ''
						})
					);
				}
			}
		} finally {
			uploading = false;
		}

		if (created.length > 0) {
			showCreated(created);
			await refreshUsage();
		}

		if (plan.skipped > 0) notes.push(t('note.skipped', { n: plan.skipped }));
		if (plan.oversize > 0) notes.push(t('note.oversize', { n: plan.oversize }));
		if (failedCount > 0) notes.push(t('note.failed', { n: failedCount }));
		if (notes.length > 0) errorMessage = notes.join('; ') + '.';
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
		// Folders can only be told apart through the entry API, and the item
		// list is emptied once this handler yields -- so sort everything into
		// folders and loose files before the first await.
		const folders: FileSystemEntry[] = [];
		const loose: File[] = [];
		for (const item of Array.from(e.dataTransfer?.items ?? [])) {
			if (item.kind !== 'file') continue;
			const entry = item.webkitGetAsEntry?.();
			if (entry?.isDirectory) {
				folders.push(entry);
			} else {
				const file = item.getAsFile();
				if (file) loose.push(file);
			}
		}
		if (folders.length === 0 && loose.length === 0) {
			const dropped = e.dataTransfer?.files;
			if (dropped && dropped.length > 0) await uploadMany(dropped);
			return;
		}
		// Each dropped folder becomes its own site.
		for (const folder of folders) {
			await uploadFolder(await pickedFromEntries([folder]));
		}
		if (loose.length > 0) await uploadMany(loose);
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

	// Runs one user action: clears the banner first, shows the failure in it, and
	// reports the server's own text only when the server actually answered.
	async function act(fallback: MessageKey, run: () => Promise<void>) {
		errorMessage = null;
		try {
			await run();
		} catch (err) {
			errorMessage = errText(err, fallback);
		}
	}

	// Every loaded copy of a file changes together: the list and, while a
	// search is showing, its results. oldSlug is the slug the row had before an
	// edit that may have changed it.
	function replaceRow(updated: FileItem, oldSlug = updated.slug) {
		rows = rows.map((f) => (f.slug === oldSlug ? updated : f));
		if (searchResults) {
			searchResults = searchResults.map((r) => (r.slug === oldSlug ? { ...r, ...updated } : r));
		}
	}

	function dropRow(slug: string) {
		rows = rows.filter((f) => f.slug !== slug);
		if (searchResults) searchResults = searchResults.filter((r) => r.slug !== slug);
	}

	// Keeps the view counts right between page loads (each page brings the
	// server's own figures).
	function countLive(f: FileItem, by: number) {
		counts.all += by;
		if (f.is_public) counts.public += by;
		else counts.private += by;
	}

	const onEmptyTrash = () => {
		if (counts.trash === 0 || emptyingTrash) return;
		if (!confirm(t('confirm.emptyTrash', { n: counts.trash }))) return;
		emptyingTrash = true;
		return act('error.emptyTrash', async () => {
			try {
				await emptyTrash();
				if (inTrash) {
					rows = [];
					nextCursor = '';
				}
				counts.trash = 0;
				await refreshUsage();
			} finally {
				emptyingTrash = false;
			}
		});
	};

	function onCreated(created: FileItem) {
		creating = false;
		// A new file lands at the top of "Recent"; leaving a search or a
		// visibility filter on would hide it and read as a failed create.
		search = '';
		if (view === 'all') {
			showCreated([created]);
		} else {
			view = 'all';
			reload();
		}
		refreshUsage();
	}

	const onRestore = (file: FileItem) =>
		act('error.restore', async () => {
			const restored = await restoreFile(file.slug);
			dropRow(file.slug);
			counts.trash -= 1;
			countLive(restored, 1);
		});

	// In the Public or Private view a file that changes sides leaves the list,
	// as it did when the filter ran in the browser.
	const onToggleVisibility = (file: FileItem) =>
		act('error.visibility', async () => {
			const updated = await setVisibility(file.slug, !file.is_public);
			countLive(file, -1);
			countLive(updated, 1);
			replaceRow(updated);
			if (!matchesView(updated)) rows = rows.filter((f) => f.slug !== updated.slug);
		});

	const onRename = (file: FileItem, name: string) =>
		act('error.rename', async () => replaceRow(await renameFile(file.slug, name)));

	const onSetTags = (file: FileItem, tags: string) =>
		act('error.saveTags', async () => replaceRow(await setTags(file.slug, tags)));

	// Every link already shared for this file stops working the moment this
	// succeeds, and there is no undo -- the old code is gone.
	const onRefreshCode = (file: FileItem) =>
		confirm(t('confirm.refreshCode', { name: file.name })) &&
		act('error.refreshCode', async () => replaceRow(await refreshCode(file.slug)));

	const onDelete = (file: FileItem) =>
		confirm(t('confirm.delete', { name: file.name })) &&
		act('error.delete', async () => {
			await deleteFile(file.slug);
			// The trash is fetched afresh whenever it is opened, so the file
			// shows up there without being carried over by hand.
			dropRow(file.slug);
			countLive(file, -1);
			counts.trash += 1;
			await refreshUsage();
		});

	const onDeletePermanent = (file: FileItem) =>
		confirm(t('confirm.deletePermanent', { name: file.name })) &&
		act('error.deletePermanent', async () => {
			await deleteFilePermanent(file.slug);
			dropRow(file.slug);
			counts.trash -= 1;
			await refreshUsage();
		});
</script>

<svelte:window
	ondragenter={onDragEnter}
	ondragover={onDragOver}
	ondragleave={onDragLeave}
	ondrop={onDrop}
/>

<svelte:head>
	<title>{viewTitle} · Renderbin</title>
</svelte:head>

{#snippet nav(mobile: boolean)}
	<ul class={mobile ? 'flex gap-1.5' : 'flex flex-col gap-0.5'}>
		{#each navItems as item (item.key)}
			<li class={mobile ? 'shrink-0' : ''}>
				<button
					onclick={() => selectView(item.key)}
					aria-current={view === item.key ? 'page' : undefined}
					class={mobile ? 'nav-chip' : 'nav-item'}
				>
					<Icon icon={item.icon} width={mobile ? 14 : 16} />
					<span class="flex-1 text-left">{item.label}</span>
					<span class="text-xs tabular-nums opacity-70">{item.count}</span>
				</button>
			</li>
		{/each}
	</ul>
	{#if !mobile}
		<div class="mt-6 border-t border-line px-1 pt-4">
			<StorageMeter {usage} />
		</div>
	{/if}
{/snippet}

{#snippet row(file: FileItem, trashed: boolean, excerpt: string | null = null)}
	<FileRow
		{file}
		{trashed}
		{excerpt}
		copied={copied.is(file.slug)}
		onCopy={copyLink}
		{onToggleVisibility}
		{onRename}
		{onSetTags}
		onEdit={(f) => (editing = f)}
		onExpiry={(f) => (expiring = f)}
		{onRefreshCode}
		{onDelete}
		{onRestore}
		{onDeletePermanent}
	/>
{/snippet}

{#snippet empty(icon: string, text: string, spin = false)}
	<div class="flex flex-col items-center gap-3 px-4 py-16 text-center">
		<Icon {icon} width="28" class="text-faint {spin ? 'animate-spin' : ''}" />
		<p class="text-sm text-muted">{text}</p>
	</div>
{/snippet}

<Shell user={data.user} {nav}>
	{#if isDraggingOver}
		<div
			class="pointer-events-none fixed inset-0 z-40 flex items-center justify-center bg-page/85 p-6"
		>
			<div
				class="flex flex-col items-center gap-3 rounded-xl border-2 border-dashed border-accent bg-surface px-12 py-10 text-center"
			>
				<Icon icon="lucide:upload-cloud" width="32" class="text-accent" />
				<p class="text-sm font-medium text-fg-strong">{t('upload.dropOverlay')}</p>
			</div>
		</div>
	{/if}

	<main class="mx-auto flex w-full max-w-[110rem] flex-col gap-4 px-3 py-4 sm:px-6 sm:py-6 lg:px-8">
		<header class="flex flex-wrap items-end justify-between gap-x-6 gap-y-3">
			<div class="min-w-0">
				<h1 class="text-xl font-bold tracking-tight text-fg-strong sm:text-2xl">{viewTitle}</h1>
				<p class="mt-0.5 font-mono text-xs text-muted">
					{#if inTrash}
						{t('count.inTrash', { n: counts.trash })}
					{:else}
						{t('count.total', { n: counts.all })} · {t('count.public', { n: counts.public })} ·
						{t('count.private', { n: counts.private })}
					{/if}
					{#if usage}
						<span class="lg:hidden">
							· {t('quota.used', {
								used: formatSize(usage.used_bytes),
								quota: formatSize(usage.quota_bytes)
							})}
						</span>
					{/if}
				</p>
			</div>

			<div class="flex flex-wrap items-center gap-2">
				{#if inTrash}
					<button
						onclick={onEmptyTrash}
						disabled={emptyingTrash || counts.trash === 0}
						title={t('trash.emptyAllTitle')}
						class="btn btn-soft hover-bad"
					>
						{#if emptyingTrash}
							<Spinner width={16} />
						{:else}
							<Icon icon="lucide:trash-2" width="16" />
						{/if}
						{t('trash.emptyAll')}
					</button>
				{:else}
					<button onclick={() => (creating = true)} class="btn btn-soft">
						<Icon icon="lucide:file-plus-2" width="16" />
						{t('create.button')}
					</button>
					<button
						onclick={() => folderInput?.click()}
						disabled={uploading}
						title={t('upload.folderTitle')}
						class="btn btn-soft max-sm:hidden"
					>
						<Icon icon="lucide:folder-up" width="16" />
						{t('upload.folder')}
					</button>
					<button onclick={() => fileInput?.click()} disabled={uploading} class="btn btn-primary">
						{#if uploading}
							<Spinner width={16} />
						{:else}
							<Icon icon="lucide:upload" width="16" />
						{/if}
						{uploading ? t('upload.uploading') : t('upload.button')}
					</button>
				{/if}
				<input
					bind:this={fileInput}
					type="file"
					accept=".html,.htm,.md,.markdown,.txt,.text,.zip,text/html,text/markdown,text/plain,application/zip"
					multiple
					class="hidden"
					onchange={onFileSelected}
				/>
				<input
					bind:this={folderInput}
					type="file"
					webkitdirectory
					class="hidden"
					onchange={onFolderSelected}
				/>
			</div>
		</header>

		{#if errorMessage}
			<Alert ondismiss={() => (errorMessage = null)}>{errorMessage}</Alert>
		{/if}

		<!-- Shown when the browser refused both clipboard paths. The input is
		     auto-selected so the link is one Ctrl+C away; without it the URL
		     appears nowhere on the page and the user has to open the file just
		     to read it out of the address bar. -->
		{#if copyFallbackUrl}
			<Alert tone="warn" icon="lucide:clipboard-x" ondismiss={() => (copyFallbackUrl = null)}>
				<div class="flex flex-wrap items-center gap-2">
					<span>{t('copy.failed')}</span>
					<!-- svelte-ignore a11y_autofocus -->
					<input
						type="text"
						readonly
						autofocus
						value={copyFallbackUrl}
						onfocus={(e) => e.currentTarget.select()}
						class="input h-8 min-w-0 flex-1 font-mono text-xs"
					/>
				</div>
			</Alert>
		{/if}

		{#if !inTrash}
			<div class="flex flex-wrap items-center gap-2">
				<SearchBar
					bind:query={search}
					bind:results={searchResults}
					bind:loading={searchLoading}
					onerror={() => (errorMessage = t('error.search'))}
				/>

				<div class="flex items-center gap-1.5">
					<div class="relative">
						<select
							value={sortKey}
							onchange={(e) => onSortChange(e.currentTarget.value as SortKey)}
							title={t('list.sort')}
							aria-label={t('list.sort')}
							class="input w-auto appearance-none pr-8"
						>
							{#each sortOptions as opt (opt.key)}
								<option value={opt.key}>{t('list.sort')}: {t(opt.labelKey)}</option>
							{/each}
						</select>
						<Icon
							icon="lucide:chevron-down"
							width="14"
							class="pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-muted"
						/>
					</div>
					{#if sortKey !== 'recent'}
						<button
							onclick={toggleSortDir}
							title={t(sortDir === 'asc' ? 'sort.ascending' : 'sort.descending')}
							aria-label={t(sortDir === 'asc' ? 'sort.ascending' : 'sort.descending')}
							class="btn btn-soft btn-icon size-9"
						>
							<Icon icon={sortDir === 'asc' ? 'lucide:arrow-up' : 'lucide:arrow-down'} width="15" />
						</button>
					{/if}
				</div>
			</div>
		{/if}

		<section
			class="card overflow-hidden transition-opacity {listLoading &&
			rows.length > 0 &&
			!searchActive
				? 'opacity-60'
				: ''}"
			aria-busy={listLoading}
		>
			{#if inTrash}
				{#if listLoading && rows.length === 0}
					{@render empty('lucide:loader-2', t('trash.loading'), true)}
				{:else if rows.length === 0}
					{@render empty('lucide:trash-2', t('trash.empty'))}
				{:else}
					<ul>
						{#each rows as file (file.slug)}
							{@render row(file, true)}
						{/each}
					</ul>
				{/if}
			{:else if searchActive}
				{#if searchLoading && searchResults === null}
					{@render empty('lucide:loader-2', t('search.loading'), true)}
				{:else if searchRows.length === 0}
					{@render empty('lucide:search-x', t('search.empty'))}
				{:else}
					<ul>
						{#each searchRows as r (r.file.slug)}
							{@render row(r.file, false, r.snippet)}
						{/each}
					</ul>
				{/if}
			{:else if listLoading && rows.length === 0}
				{@render empty('lucide:loader-2', t('files.loading'), true)}
			{:else if rows.length === 0}
				{@render empty(
					'lucide:inbox',
					counts.all === 0 ? t('files.emptyNone') : t('files.emptyFiltered')
				)}
			{:else if sortKey === 'recent'}
				{#each groups as group (group.label)}
					<div
						class="border-b border-line bg-raised/50 px-4 py-1.5 text-xs font-semibold tracking-wide text-muted uppercase"
					>
						{group.label}
					</div>
					<ul>
						{#each group.items as file (file.slug)}
							{@render row(file, false)}
						{/each}
					</ul>
				{/each}
			{:else}
				<ul>
					{#each rows as file (file.slug)}
						{@render row(file, false)}
					{/each}
				</ul>
			{/if}
			{#if nextCursor && !searchActive && rows.length > 0}
				<div {@attach nearEnd} class="flex justify-center border-t border-line p-3">
					<button onclick={loadMore} disabled={loadingMore} class="btn btn-ghost btn-sm">
						{#if loadingMore}
							<Spinner width={14} />
						{/if}
						{t('list.loadMore')}
					</button>
				</div>
			{/if}
		</section>

		{#if !inTrash}
			<p class="text-center text-xs text-faint max-sm:hidden">{t('upload.hint')}</p>
		{/if}
	</main>
</Shell>

{#if creating}
	<CreateModal onclose={() => (creating = false)} oncreated={onCreated} />
{/if}

{#if editing}
	<EditModal
		file={editing}
		onclose={() => (editing = null)}
		onsaved={(updated) => {
			// Match on the slug the row had when the modal opened: the edit may have changed it.
			replaceRow(updated, editing?.slug);
			editing = null;
		}}
		onreplaced={(updated) => {
			replaceRow(updated);
			refreshUsage();
		}}
	/>
{/if}

{#if expiring}
	<ExpiryModal
		file={expiring}
		onclose={() => (expiring = null)}
		onsaved={(updated) => {
			replaceRow(updated);
			expiring = null;
		}}
	/>
{/if}
