<script lang="ts">
	// API-backed search over the current user's own files: 250ms debounce,
	// name-only by default, optionally including file contents. The query and the
	// outcome are bound by the page (which swaps its list for the results and can
	// clear the box); the content toggle is this box's own business.
	import Icon from '$lib/components/Icon.svelte';
	import Spinner from '$lib/components/Spinner.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { searchFiles, type SearchResult } from '$lib/api/files';

	let {
		query = $bindable(''),
		// eslint-disable-next-line no-useless-assignment -- written here, read by the page through the binding
		results = $bindable(),
		loading = $bindable(false),
		onerror
	}: {
		query: string;
		// null while there is no query; otherwise the latest answer.
		results: SearchResult[] | null;
		loading: boolean;
		onerror: () => void;
	} = $props();

	let content = $state(false);
	let latest = 0; // each run invalidates the ones before it, so a stale answer is dropped

	$effect(() => {
		const q = query.trim();
		const withContent = content;
		const mine = ++latest;
		if (!q) {
			results = null;
			loading = false;
			return;
		}
		loading = true;
		const timer = setTimeout(async () => {
			try {
				const found = await searchFiles(q, withContent);
				if (mine === latest) results = found;
			} catch {
				if (mine === latest) {
					results = [];
					onerror();
				}
			} finally {
				if (mine === latest) loading = false;
			}
		}, 250);
		return () => clearTimeout(timer);
	});
</script>

<div
	class="flex h-9 min-w-0 flex-[1_1_16rem] items-stretch overflow-hidden rounded-md border border-line bg-surface focus-within:border-accent"
>
	<div class="relative min-w-0 flex-1">
		<Icon
			icon="lucide:search"
			width="15"
			class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-muted"
		/>
		<input
			type="search"
			bind:value={query}
			placeholder={t('list.searchPlaceholder')}
			title={t('search.scopeTitle')}
			aria-label={t('list.searchPlaceholder')}
			class="h-full w-full bg-transparent pr-14 pl-9 text-sm text-fg outline-none placeholder:text-faint [&::-webkit-search-cancel-button]:appearance-none"
		/>
		<div class="absolute top-1/2 right-1.5 flex -translate-y-1/2 items-center gap-0.5">
			{#if loading}
				<Spinner width={14} class="text-muted" />
			{/if}
			{#if query}
				<button
					onclick={() => (query = '')}
					title={t('search.clear')}
					aria-label={t('search.clear')}
					class="rounded p-1 text-muted hover:bg-raised hover:text-fg"
				>
					<Icon icon="lucide:x" width="14" />
				</button>
			{/if}
		</div>
	</div>
	<button
		onclick={() => (content = !content)}
		title={t('search.contentTitle')}
		aria-pressed={content}
		class="flex shrink-0 items-center gap-1.5 border-l border-line px-3 text-xs font-medium transition-colors {content
			? 'bg-accent text-on-accent'
			: 'text-muted hover:bg-raised hover:text-fg'}"
	>
		<Icon icon="lucide:file-search" width="14" />
		<span class="max-sm:hidden">{t('search.content')}</span>
	</button>
</div>
