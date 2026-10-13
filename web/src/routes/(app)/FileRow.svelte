<script lang="ts">
	// One row of the file list, for live and trashed files alike. Inline title and
	// tag editing is local state; everything that touches the server is a callback,
	// so the page stays the only owner of the lists and of error reporting.
	import Icon from '$lib/components/Icon.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { formatSize } from '$lib/format';
	import type { FileItem } from '$lib/api/files';
	import { formatDeadline, formatTime, fullDate } from '$lib/dates';
	import { downloadName, kindIcon, kindLabel, resUrl } from '$lib/files-ui';
	import InlineEdit from './InlineEdit.svelte';

	let {
		file,
		trashed = false,
		excerpt = null,
		copied = false,
		onCopy,
		onToggleVisibility,
		onRename,
		onSetTags,
		onEdit,
		onExpiry,
		onRefreshCode,
		onDelete,
		onRestore,
		onDeletePermanent
	}: {
		file: FileItem;
		trashed?: boolean;
		// Content-search hit: the matched passage, shown de-emphasized.
		excerpt?: string | null;
		copied?: boolean;
		onCopy: (file: FileItem) => void;
		onToggleVisibility: (file: FileItem) => void;
		onRename: (file: FileItem, name: string) => Promise<void>;
		onSetTags: (file: FileItem, tags: string) => Promise<void>;
		onEdit: (file: FileItem) => void;
		onExpiry: (file: FileItem) => void;
		onRefreshCode: (file: FileItem) => void;
		onDelete: (file: FileItem) => void;
		onRestore: (file: FileItem) => void;
		onDeletePermanent: (file: FileItem) => void;
	} = $props();

	let editingTitle = $state(false);
	let titleValue = $state('');
	let editingTags = $state(false);
	let tagsValue = $state('');

	const tags = $derived(
		file.tags
			? file.tags
					.split(',')
					.map((s) => s.trim())
					.filter(Boolean)
			: []
	);

	function startTitle() {
		titleValue = file.name;
		editingTitle = true;
	}

	async function saveTitle() {
		const name = titleValue.trim();
		editingTitle = false;
		if (name && name !== file.name) await onRename(file, name);
	}

	function startTags() {
		tagsValue = tags.join(',');
		editingTags = true;
	}

	// Mirrors the backend's normalizeTags (trim, drop empties, dedupe) so the chips
	// shown after save match what was typed; also accepts full-width commas, which
	// the backend doesn't split on.
	function normalizeTags(value: string): string {
		return [
			...new Set(
				value
					.split(/[,，]/)
					.map((s) => s.trim())
					.filter(Boolean)
			)
		].join(',');
	}

	async function saveTags() {
		const next = normalizeTags(tagsValue);
		editingTags = false;
		if (next !== tags.join(',')) await onSetTags(file, next);
	}
</script>

{#snippet stat(icon: string, n: number, tone: string, title: string)}
	<span
		class="inline-flex items-center gap-1 {n > 0 ? tone : 'text-muted'}"
		{title}
		aria-label="{title}: {n}"
	>
		<Icon {icon} width="13" />{n}
	</span>
{/snippet}

{#snippet iconBtn(icon: string, label: string, onclick: () => void, extra = '')}
	<button {onclick} title={label} aria-label={label} class="btn btn-ghost btn-icon {extra}">
		<Icon {icon} width="16" />
	</button>
{/snippet}

{#snippet stats()}
	{@render stat('lucide:check-circle-2', file.success_count, 'text-ok', t('row.successfulViews'))}
	{@render stat('lucide:key-round', file.code_success_count, 'text-warn', t('row.codeViews'))}
	{@render stat('lucide:x-circle', file.failure_count, 'text-bad', t('row.failedViews'))}
{/snippet}

<li
	class="group flex flex-col gap-2.5 border-b border-line px-3 py-2.5 last:border-0 hover:bg-raised/40 sm:px-4 xl:flex-row xl:items-center xl:gap-6"
>
	<div class="flex min-w-0 flex-1 items-start gap-3">
		<span
			class="mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-md {file.is_public
				? 'bg-ok/15 text-ok'
				: 'bg-raised text-muted'}"
		>
			<Icon icon={kindIcon(file.kind)} width="18" />
		</span>

		<div class="min-w-0 flex-1">
			<div class="flex min-h-6 items-center gap-1.5">
				{#if trashed}
					<span class="truncate font-medium text-muted">{file.name}</span>
				{:else if editingTitle}
					<InlineEdit
						bind:value={titleValue}
						label={t('edit.titleLabel')}
						class="min-w-0 flex-1"
						onsave={saveTitle}
						oncancel={() => (editingTitle = false)}
					/>
				{:else}
					<!-- eslint-disable svelte/no-navigation-without-resolve -- /res/[slug] is served by the Go backend, not a SvelteKit route -->
					<a
						href={resUrl(file)}
						target="_blank"
						rel="noopener"
						data-sveltekit-reload
						class="truncate font-medium text-fg-strong hover:text-accent hover:underline"
					>
						{file.name}
					</a>
					<!-- eslint-enable svelte/no-navigation-without-resolve -->
					<button
						onclick={startTitle}
						title={t('row.editTitle')}
						aria-label={t('row.editTitle')}
						class="btn btn-ghost btn-icon size-6 text-faint"
					>
						<Icon icon="lucide:pencil" width="12" />
					</button>
				{/if}
			</div>

			<div class="mt-1 flex flex-wrap items-center gap-x-2.5 gap-y-1.5 text-xs text-muted">
				<span
					class="chip {file.kind === 'site' ? 'chip-info' : 'chip-neutral'} font-mono"
					title={t('row.formatTitle', { kind: kindLabel(file.kind) })}
				>
					{kindLabel(file.kind)}
				</span>
				<span class="font-mono" title={t('row.sizeTitle')}>{formatSize(file.size)}</span>
				<span title={fullDate(file.created_at)}>{formatTime(file.created_at)}</span>
				{#if file.updated_at !== file.created_at}
					<span title={fullDate(file.updated_at)}>
						{t('row.updated', { time: formatTime(file.updated_at) })}
					</span>
				{/if}
				{#if file.expires_at}
					<span
						class="chip chip-warn"
						title={t('row.expiresTitle', { datetime: fullDate(file.expires_at) })}
					>
						<Icon icon="lucide:clock" width="12" />
						{t('row.expires', { datetime: formatDeadline(file.expires_at) })}
					</span>
				{/if}
				{#if file.max_views != null}
					<span class="chip chip-warn" title={t('row.viewsLeftTitle')}>
						<Icon icon="lucide:eye" width="12" />
						{t('row.viewsLeft', { n: Math.max(0, file.max_views - (file.view_count ?? 0)) })}
					</span>
				{/if}
				<!-- Why this link stopped working. Expiring clears the limit columns, so
				     without this the row could only say "private" and the owner couldn't
				     tell an expired link from one they made private themselves. Shown
				     only while the file is still private: re-publishing it makes the note
				     stale, and setting a new limit clears it server-side. -->
				{#if !file.is_public && file.expired_reason}
					<span
						class="chip chip-bad"
						title={t('row.expiredTitle', { datetime: fullDate(file.expired_at ?? '') })}
					>
						<Icon icon="lucide:circle-slash" width="12" />
						{file.expired_reason === 'ttl' ? t('row.expiredTtl') : t('row.expiredViews')}
					</span>
				{/if}
				<span class="inline-flex items-center gap-2.5 tabular-nums xl:hidden">
					{@render stats()}
				</span>
				{#if editingTags}
					<InlineEdit
						bind:value={tagsValue}
						label={t('row.editTags')}
						placeholder={t('row.tagsPlaceholder')}
						class="w-56 max-w-full text-xs"
						onsave={saveTags}
						oncancel={() => (editingTags = false)}
					/>
				{:else}
					{#each tags as tag (tag)}
						<span class="chip chip-neutral gap-0.5 rounded-full pl-2">
							{tag}
							{#if !trashed}
								<button
									onclick={() => onSetTags(file, tags.filter((x) => x !== tag).join(','))}
									title={t('row.removeTag', { tag })}
									aria-label={t('row.removeTag', { tag })}
									class="rounded-full p-0.5 text-muted hover:text-bad"
								>
									<Icon icon="lucide:x" width="10" />
								</button>
							{:else}
								<span class="pr-1.5"></span>
							{/if}
						</span>
					{/each}
					{#if !trashed}
						<button
							onclick={startTags}
							title={t('row.editTags')}
							class="inline-flex items-center gap-0.5 rounded-full border border-dashed border-line-strong px-2 py-0.5 text-[11px] text-muted hover:border-accent hover:text-accent"
						>
							<Icon icon={tags.length > 0 ? 'lucide:pencil' : 'lucide:plus'} width="10" />
							{t('row.tag')}
						</button>
					{/if}
				{/if}
			</div>

			{#if excerpt}
				<p class="mt-1 line-clamp-2 text-xs break-all text-muted">{excerpt}</p>
			{/if}
		</div>
	</div>

	<div
		class="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 xl:shrink-0 xl:justify-end"
	>
		<div class="hidden items-center gap-3 text-xs tabular-nums xl:flex xl:w-36">
			{@render stats()}
		</div>

		{#if trashed}
			<div class="flex items-center gap-1.5">
				<button onclick={() => onRestore(file)} class="btn btn-soft btn-sm">
					<Icon icon="lucide:rotate-ccw" width="14" />
					{t('row.restore')}
				</button>
				<button onclick={() => onDeletePermanent(file)} class="btn btn-soft btn-sm hover-bad">
					<Icon icon="lucide:trash-2" width="14" />
					{t('row.deletePermanent')}
				</button>
			</div>
		{:else}
			<div class="flex flex-wrap items-center gap-2">
				<button
					onclick={() => onCopy(file)}
					title={t('row.copyLink')}
					class="chip chip-neutral h-7 gap-1.5 px-2 font-mono hover:text-accent"
				>
					<Icon icon={copied ? 'lucide:check' : 'lucide:copy'} width="12" />
					{file.access_code}
				</button>
				<button
					role="switch"
					aria-checked={file.is_public}
					aria-label={t('row.toggleVisibility', { name: file.name })}
					onclick={() => onToggleVisibility(file)}
					class="inline-flex h-7 min-w-[4.75rem] items-center justify-center gap-1.5 rounded-full border px-2.5 text-xs font-medium transition-colors {file.is_public
						? 'border-ok/40 bg-ok/15 text-ok'
						: 'border-line bg-raised text-muted hover:text-fg'}"
				>
					<span class="size-1.5 rounded-full bg-current"></span>
					{file.is_public ? t('row.public') : t('row.private')}
				</button>
				<span class="flex items-center">
					<!-- eslint-disable svelte/no-navigation-without-resolve -- /api/files/[slug]/download is served by the Go backend, not a SvelteKit route -->
					<a
						href={`/api/files/${file.slug}/download`}
						download={downloadName(file)}
						title={t('row.downloadSource')}
						aria-label={t('row.downloadSource')}
						class="btn btn-ghost btn-icon"
					>
						<Icon icon="lucide:download" width="16" />
					</a>
					<!-- eslint-enable svelte/no-navigation-without-resolve -->
					{@render iconBtn('lucide:file-pen', t('row.edit'), () => onEdit(file))}
					{@render iconBtn('lucide:clock', t('row.setExpiry'), () => onExpiry(file))}
					{@render iconBtn('lucide:refresh-cw', t('row.refreshCode'), () => onRefreshCode(file))}
					{@render iconBtn('lucide:trash-2', t('row.delete'), () => onDelete(file), 'hover-bad')}
				</span>
			</div>
		{/if}
	</div>
</li>
