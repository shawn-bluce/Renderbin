<script lang="ts">
	// Backup and restore, rendered inline in Settings (super admin only) — the
	// backup is the whole instance (the database plus every file's content, as
	// one .tar), so it carries every account's files, password hashes and API
	// keys, and restoring it overwrites all of them.
	import Alert from '$lib/components/Alert.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import SectionTitle from '$lib/components/SectionTitle.svelte';
	import Spinner from '$lib/components/Spinner.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { formatSize } from '$lib/format';
	import { ApiError } from '$lib/api/http';
	import { restoreBackup } from '$lib/api/settings';

	let fileInput = $state<HTMLInputElement | null>(null);
	let pending = $state<File | null>(null);
	let busy = $state(false);
	let errorMessage = $state<string | null>(null);
	let done = $state<{ users: number; files: number } | null>(null);

	function onFileSelected(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		pending = input.files?.[0] ?? null;
		errorMessage = null;
		done = null;
		// Cleared so picking the same file twice still fires a change event.
		input.value = '';
	}

	async function confirmRestore() {
		if (!pending || busy) return;
		errorMessage = null;
		busy = true;
		try {
			const result = await restoreBackup(pending);
			done = result;
			pending = null;
			// The restore replaced the sessions table along with everything else,
			// so this session is almost certainly gone and every piece of loaded
			// state is stale. A full reload re-runs the layout's auth guard, which
			// lands on the login page when that is the truth.
			setTimeout(() => location.reload(), 1500);
		} catch (err) {
			// The server describes what it rejected (not a backup, no accounts,
			// schema mismatch, cut short, not enough disk space) and nothing was
			// changed in any of those cases.
			errorMessage =
				err instanceof ApiError && [400, 411, 507].includes(err.status)
					? err.message
					: t('error.restore.failed');
		} finally {
			busy = false;
		}
	}
</script>

<section id="backup" class="card scroll-mt-20 p-5">
	<SectionTitle icon="lucide:database-backup" label={t('backup.title')} />

	<div class="mt-4 flex flex-wrap items-center justify-between gap-3">
		<div class="min-w-0 flex-1">
			<p class="text-sm text-fg">{t('backup.download')}</p>
			<p class="hint">{t('backup.downloadHint')}</p>
		</div>
		<!-- eslint-disable svelte/no-navigation-without-resolve -- /api/backup is served by the Go backend, not a SvelteKit route -->
		<a href="/api/backup" download class="btn btn-soft shrink-0">
			<Icon icon="lucide:download" width="15" />
			{t('backup.downloadButton')}
		</a>
		<!-- eslint-enable svelte/no-navigation-without-resolve -->
	</div>

	<div class="mt-4 border-t border-line pt-4">
		<p class="text-sm text-fg">{t('backup.restore')}</p>
		<p class="hint">{t('backup.restoreHint')}</p>

		<div class="mt-3 flex flex-wrap items-center gap-2">
			<button onclick={() => fileInput?.click()} disabled={busy} class="btn btn-soft">
				<Icon icon="lucide:upload" width="15" />
				{t('backup.chooseFile')}
			</button>
			<input
				bind:this={fileInput}
				type="file"
				accept=".tar,.db,application/x-tar,application/octet-stream"
				class="hidden"
				onchange={onFileSelected}
			/>
			{#if pending}
				<span class="font-mono text-xs break-all text-muted">
					{pending.name} · {formatSize(pending.size)}
				</span>
			{/if}
		</div>

		{#if pending}
			<!-- A second, deliberate step: this button overwrites every account's
			     data, and the file picker is one misclick. -->
			<Alert tone="warn" class="mt-3">
				<div class="flex flex-wrap items-center justify-between gap-3">
					<p class="min-w-0 flex-1 basis-60 text-xs">{t('backup.restoreWarning')}</p>
					<div class="flex shrink-0 items-center gap-2">
						<button onclick={() => (pending = null)} disabled={busy} class="btn btn-ghost btn-sm">
							{t('common.cancel')}
						</button>
						<button onclick={confirmRestore} disabled={busy} class="btn btn-danger btn-sm">
							{#if busy}
								<Spinner width={13} />
							{/if}
							{busy ? t('backup.restoring') : t('backup.restoreButton')}
						</button>
					</div>
				</div>
			</Alert>
		{/if}

		{#if errorMessage}
			<Alert class="mt-3">{errorMessage}</Alert>
		{/if}
		{#if done}
			<Alert tone="ok" icon="lucide:loader-2" iconClass="animate-spin" class="mt-3">
				{t('backup.restored', { users: done.users, files: done.files })}
			</Alert>
		{/if}
	</div>
</section>
