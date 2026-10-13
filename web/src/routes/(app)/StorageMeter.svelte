<script lang="ts">
	// Stored bytes against the account's quota. Fills from accent to warn to bad as
	// the limit nears; the figure counts trashed files too, like the upload check.
	import Icon from '$lib/components/Icon.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { formatSize } from '$lib/format';
	import type { Usage } from '$lib/api/settings';

	let { usage }: { usage: Usage | null } = $props();

	const pct = $derived(
		usage && usage.quota_bytes > 0 ? Math.min(100, (usage.used_bytes / usage.quota_bytes) * 100) : 0
	);
</script>

{#if usage}
	<div
		class="flex flex-col gap-1.5"
		title={t('quota.used', {
			used: formatSize(usage.used_bytes),
			quota: formatSize(usage.quota_bytes)
		})}
	>
		<div class="flex items-center justify-between text-xs text-muted">
			<span class="inline-flex items-center gap-1.5">
				<Icon icon="lucide:hard-drive" width="13" />{t('quota.title')}
			</span>
			<span class="font-mono tabular-nums">
				{formatSize(usage.used_bytes)} / {formatSize(usage.quota_bytes)}
			</span>
		</div>
		<div
			class="h-1.5 overflow-hidden rounded-full bg-raised"
			role="progressbar"
			aria-label={t('quota.title')}
			aria-valuemin="0"
			aria-valuemax="100"
			aria-valuenow={Math.round(pct)}
		>
			<div
				class="h-full rounded-full {pct >= 95 ? 'bg-bad' : pct >= 80 ? 'bg-warn' : 'bg-accent'}"
				style:width="{pct}%"
			></div>
		</div>
	</div>
{/if}
