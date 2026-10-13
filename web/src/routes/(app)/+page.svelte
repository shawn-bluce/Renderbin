<script lang="ts">
	import Icon from '$lib/components/Icon.svelte';
	import Alert from '$lib/components/Alert.svelte';
	import Shell from '$lib/components/Shell.svelte';
	import Spinner from '$lib/components/Spinner.svelte';
	import { t, intlLocale } from '$lib/i18n/index.svelte';
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
		listTrashed,
		refreshCode,
		renameFile,
		restoreFile,
		setTags,
		setVisibility,
		uploadSite,
		type FileItem,
		type SearchResult
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

	// Intentional one-time snapshots: `files` is mutated locally for optimistic
	// updates and shouldn't re-sync if `data` changes.
	// svelte-ignore state_referenced_locally
	let files = $state<FileItem[]>(data.files);
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
	// filter over the live files, or the trash. The trash count is only known
	// once the trash has been opened -- listing it costs a request.
	type View = 'all' | 'public' | 'private' | 'trash';
	let view = $state<View>('all');
	const inTrash = $derived(view === 'trash');
	let trashedFiles = $state<FileItem[]>([]);
	let trashedLoaded = $state(false);
	let trashLoading = $state(false);
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

	type SortKey = 'recent' | 'name' | 'success' | 'failure';
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
	}

	const totalCount = $derived(files.length);
	const publicCount = $derived(files.filter((f) => f.is_public).length);
	const privateCount = $derived(totalCount - publicCount);

	const navItems = $derived<{ key: View; icon: string; label: string; count: number | null }[]>([
		{ key: 'all', icon: 'lucide:layout-list', label: t('nav.all'), count: totalCount },
		{ key: 'public', icon: 'lucide:globe', label: t('visibility.public'), count: publicCount },
		{ key: 'private', icon: 'lucide:lock', label: t('visibility.private'), count: privateCount },
		{
			key: 'trash',
			icon: 'lucide:trash-2',
			label: t('tab.trashed'),
			count: trashedLoaded ? trashedFiles.length : null
		}
	]);
	const viewTitle = $derived(navItems.find((i) => i.key === view)!.label);

	async function selectView(next: View) {
		view = next;
		if (next !== 'trash' || trashedLoaded || trashLoading) return;
		trashLoading = true;
		try {
			trashedFiles = await listTrashed();
			trashedLoaded = true;
		} catch (err) {
			errorMessage = errText(err, 'error.loadTrash');
		} finally {
			trashLoading = false;
		}
	}

	function passesVisibility(f: FileItem): boolean {
		if (view === 'public') return f.is_public;
		if (view === 'private') return !f.is_public;
		return true;
	}

	const filteredFiles = $derived(files.filter(passesVisibility));

	// Grouped-by-day view, used only for the default "Recent" sort.
	const groups = $derived.by(() => {
		const order: string[] = [];
		// eslint-disable-next-line svelte/prefer-svelte-reactivity -- a local, rebuilt on every derivation
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

	async function copyLink(file: FileItem) {
		copyFallbackUrl = null;
		const text = resUrl(file);
		if (!(await copied.copy(text, file.slug))) copyFallbackUrl = text;
	}

	// New files land in the live list, so make sure that is what is on screen.
	function showLive() {
		if (view === 'trash') view = 'all';
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
			const created = await uploadSite(null, zip);
			files = [created, ...files];
			showLive();
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
			files = [...created, ...files];
			showLive();
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

	function replaceRow(updated: FileItem) {
		files = files.map((f) => (f.slug === updated.slug ? updated : f));
	}

	const onEmptyTrash = () => {
		if (trashedFiles.length === 0 || emptyingTrash) return;
		if (!confirm(t('confirm.emptyTrash', { n: trashedFiles.length }))) return;
		emptyingTrash = true;
		return act('error.emptyTrash', async () => {
			try {
				await emptyTrash();
				trashedFiles = [];
				await refreshUsage();
			} finally {
				emptyingTrash = false;
			}
		});
	};

	function onCreated(created: FileItem) {
		files = [created, ...files];
		creating = false;
		// A new file lands at the top of "Recent"; leaving a search or a
		// visibility filter on would hide it and read as a failed create.
		search = '';
		view = 'all';
		refreshUsage();
	}

	const onRestore = (file: FileItem) =>
		act('error.restore', async () => {
			const restored = await restoreFile(file.slug);
			trashedFiles = trashedFiles.filter((f) => f.slug !== file.slug);
			files = [restored, ...files];
		});

	const onToggleVisibility = (file: FileItem) =>
		act('error.visibility', async () =>
			replaceRow(await setVisibility(file.slug, !file.is_public))
		);

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
			files = files.filter((f) => f.slug !== file.slug);
			// Move it into the trash list too. Without this the trash kept whatever
			// it fetched the first time it was opened (trashedLoaded latches), so a
			// file deleted afterwards was missing from both lists and looked like a
			// soft delete had destroyed it.
			if (trashedLoaded) trashedFiles = [file, ...trashedFiles];
			await refreshUsage();
		});

	const onDeletePermanent = (file: FileItem) =>
		confirm(t('confirm.deletePermanent', { name: file.name })) &&
		act('error.deletePermanent', async () => {
			await deleteFilePermanent(file.slug);
			trashedFiles = trashedFiles.filter((f) => f.slug !== file.slug);
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
					{#if item.count !== null}
						<span class="text-xs tabular-nums opacity-70">{item.count}</span>
					{/if}
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
						{t('count.inTrash', { n: trashedFiles.length })}
					{:else}
						{t('count.total', { n: totalCount })} · {t('count.public', { n: publicCount })} ·
						{t('count.private', { n: privateCount })}
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
						disabled={emptyingTrash || trashedFiles.length === 0}
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
							onclick={() => (sortDir = sortDir === 'asc' ? 'desc' : 'asc')}
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

		<section class="card overflow-hidden">
			{#if inTrash}
				{#if trashLoading}
					{@render empty('lucide:loader-2', t('trash.loading'), true)}
				{:else if trashedFiles.length === 0}
					{@render empty('lucide:trash-2', t('trash.empty'))}
				{:else}
					<ul>
						{#each trashedFiles as file (file.slug)}
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
			{:else if filteredFiles.length === 0}
				{@render empty(
					'lucide:inbox',
					files.length === 0 ? t('files.emptyNone') : t('files.emptyFiltered')
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
					{#each sortedFlat as file (file.slug)}
						{@render row(file, false)}
					{/each}
				</ul>
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
			const oldSlug = editing?.slug;
			files = files.map((f) => (f.slug === oldSlug ? updated : f));
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
