<script lang="ts">
	// Full edit: title / slug / access code / source. A site has no source to edit:
	// the modal lists its files instead and offers to replace them wholesale, right
	// away rather than on Save.
	import Alert from '$lib/components/Alert.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import Modal from '$lib/components/Modal.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { formatSize } from '$lib/format';
	import { pickedFromInput, type PickedFile } from '$lib/site';
	import { ApiError } from '$lib/api/http';
	import { getFile, replaceSite, updateFile, type FileItem, type SiteEntry } from '$lib/api/files';
	import { errText } from '$lib/errors';
	import { validateDoc, zipFolder, zipTooLarge } from '$lib/files-ui';

	let {
		file,
		onclose,
		onsaved,
		onreplaced
	}: {
		file: FileItem;
		onclose: () => void;
		// Save: the modal is done.
		onsaved: (file: FileItem) => void;
		// A site's files were swapped: the list (and quota) must catch up, but the modal stays.
		onreplaced: (file: FileItem) => void;
	} = $props();

	// The row this modal was opened for; later changes to `file` don't re-seed the form.
	/* svelte-ignore state_referenced_locally */
	const original = file;
	const isSite = original.kind === 'site';

	let title = $state(original.name);
	let slug = $state(original.slug);
	let code = $state(original.access_code);
	let content = $state('');
	let entries = $state<SiteEntry[]>([]);
	// The row carries no source: it is fetched on open, and saving waits for it
	// (a Save before it lands would overwrite the document with an empty string).
	let loading = $state(true);
	let busy = $state(false);
	let error = $state<string | null>(null);
	let notice = $state<string | null>(null);
	let zipInput = $state<HTMLInputElement | null>(null);
	let folderInput = $state<HTMLInputElement | null>(null);

	$effect(() => {
		getFile(original.slug)
			.then((full) => {
				content = full.html_content ?? '';
				entries = full.entries ?? [];
			})
			.catch((err) => (error = errText(err, 'error.loadContent')))
			.finally(() => (loading = false));
	});

	// '.' and '..' pass the charset but would vanish from a site's URL, where the
	// slug and the code are both path segments.
	const validToken = (v: string) => /^[A-Za-z0-9._-]{1,128}$/.test(v) && v !== '.' && v !== '..';

	async function replaceWith(source: File | PickedFile[]) {
		if (busy || loading) return;
		error = null;
		notice = null;
		busy = true;
		try {
			const zip = Array.isArray(source) ? await zipFolder(source) : (zipTooLarge(source) ?? source);
			if (typeof zip === 'string') {
				error = zip;
				return;
			}
			const updated = await replaceSite(original.slug, zip);
			entries = updated.entries ?? [];
			onreplaced({ ...updated, entries: undefined });
			notice = t('site.replaced');
		} catch (err) {
			error = errText(err, 'error.save');
		} finally {
			busy = false;
		}
	}

	async function onZip(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		const picked = input.files?.[0];
		input.value = '';
		if (picked) await replaceWith(picked);
	}

	async function onFolder(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		const picked = input.files && input.files.length > 0 ? pickedFromInput(input.files) : null;
		input.value = '';
		if (picked) await replaceWith(picked);
	}

	async function save() {
		const name = title.trim() || t('untitled');
		const newSlug = slug.trim();
		if (!validToken(newSlug)) {
			error = t('error.slug');
			return;
		}
		const access_code = code.trim();
		if (!validToken(access_code)) {
			error = t('error.accessCode');
			return;
		}
		if (!isSite) {
			const invalid = validateDoc(content);
			if (invalid) {
				error = t(invalid);
				return;
			}
		}
		error = null;
		busy = true;
		try {
			onsaved(
				await updateFile(original.slug, {
					name,
					slug: newSlug,
					...(isSite ? {} : { html_content: content }),
					access_code
				})
			);
		} catch (err) {
			// The only 409 this endpoint produces is a slug collision.
			error =
				err instanceof ApiError && err.status === 409
					? t('error.slugTaken')
					: errText(err, 'error.save');
			busy = false;
		}
	}

	const sourceLabel = $derived(
		original.kind === 'markdown'
			? t('source.markdown')
			: original.kind === 'txt'
				? t('source.txt')
				: t('source.html')
	);
</script>

<Modal title={t('edit.title')} {onclose} {busy} wide onsubmit={save} submitDisabled={loading}>
	<label class="field">
		<span class="label">{t('edit.titleLabel')}</span>
		<input bind:value={title} class="input" />
	</label>

	<div class="grid gap-4 sm:grid-cols-2">
		<label class="field">
			<span class="label">{t('edit.slugLabel')}</span>
			<input bind:value={slug} spellcheck="false" class="input font-mono" />
		</label>
		<label class="field">
			<span class="label">{t('edit.codeLabel')}</span>
			<input bind:value={code} spellcheck="false" class="input font-mono" />
		</label>
	</div>

	{#if isSite}
		<div class="flex min-h-0 flex-col gap-2">
			<span class="label">{t('site.files', { n: entries.length })}</span>
			<ul class="max-h-56 min-h-16 panel overflow-auto py-1 font-mono text-xs">
				{#each entries as entry (entry.path)}
					<li class="flex items-center justify-between gap-4 px-3 py-0.5">
						<span class="truncate">{entry.path}</span>
						<span class="shrink-0 text-muted">{formatSize(entry.size)}</span>
					</li>
				{/each}
			</ul>
			<div class="flex flex-wrap items-center gap-2">
				<button
					onclick={() => folderInput?.click()}
					disabled={busy || loading}
					class="btn btn-soft btn-sm"
				>
					<Icon icon="lucide:folder-up" width="14" />
					{t('site.replaceFolder')}
				</button>
				<button
					onclick={() => zipInput?.click()}
					disabled={busy || loading}
					class="btn btn-soft btn-sm"
				>
					<Icon icon="lucide:file-archive" width="14" />
					{t('site.replaceZip')}
				</button>
				<input
					bind:this={folderInput}
					type="file"
					webkitdirectory
					class="hidden"
					onchange={onFolder}
				/>
				<input
					bind:this={zipInput}
					type="file"
					accept=".zip,application/zip"
					class="hidden"
					onchange={onZip}
				/>
			</div>
			<p class="text-xs text-muted">{t('site.replaceHint')}</p>
			{#if notice}
				<p class="text-xs text-ok">{notice}</p>
			{/if}
		</div>
	{:else}
		<label class="flex min-h-0 flex-1 flex-col gap-1.5">
			<span class="label">{sourceLabel}</span>
			<textarea
				bind:value={content}
				disabled={loading || busy}
				spellcheck="false"
				class="input min-h-56 flex-1 resize-none font-mono text-xs sm:min-h-72"></textarea>
		</label>
	{/if}

	{#if error}
		<Alert>{error}</Alert>
	{/if}
</Modal>
