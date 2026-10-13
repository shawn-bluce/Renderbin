<script lang="ts">
	// New-file modal. `kind` is only ever chosen here: the format decides how
	// /res/{slug} renders the source and is fixed at creation, so the edit modal
	// offers no way to change it.
	import Alert from '$lib/components/Alert.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import Modal from '$lib/components/Modal.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import type { MessageKey } from '$lib/i18n/messages';
	import { createFile, type FileItem, type FileKind } from '$lib/api/files';
	import { errText } from '$lib/errors';
	import { focusOnMount, kindIcon, validateDoc } from '$lib/files-ui';

	let { onclose, oncreated }: { onclose: () => void; oncreated: (file: FileItem) => void } =
		$props();

	const kinds = [
		{ key: 'markdown', labelKey: 'kind.markdown' },
		{ key: 'html', labelKey: 'kind.html' },
		{ key: 'txt', labelKey: 'kind.txt' }
	] as const satisfies { key: FileKind; labelKey: MessageKey }[];

	let name = $state('');
	let kind = $state<FileKind>('markdown');
	let content = $state('');
	let busy = $state(false);
	let error = $state<string | null>(null);

	async function submit() {
		const invalid = validateDoc(content);
		if (invalid) {
			error = t(invalid);
			return;
		}
		error = null;
		busy = true;
		try {
			oncreated(await createFile(name.trim() || t('untitled'), content, kind));
		} catch (err) {
			error = errText(err, 'error.create');
			busy = false;
		}
	}
</script>

<Modal
	title={t('create.title')}
	{onclose}
	{busy}
	wide
	onsubmit={submit}
	submitLabel={t('create.submit')}
	busyLabel={t('create.creating')}
>
	<div class="grid gap-4 sm:grid-cols-[1fr_auto]">
		<label class="field">
			<span class="label">{t('create.nameLabel')}</span>
			<input bind:value={name} use:focusOnMount placeholder={t('untitled')} class="input" />
		</label>
		<div class="field">
			<span class="label">{t('create.kindLabel')}</span>
			<div role="group" aria-label={t('create.kindLabel')} class="seg">
				{#each kinds as opt (opt.key)}
					<button onclick={() => (kind = opt.key)} aria-pressed={kind === opt.key}>
						<Icon icon={kindIcon(opt.key)} width="13" />
						{t(opt.labelKey)}
					</button>
				{/each}
			</div>
		</div>
	</div>
	<p class="-mt-2 text-xs text-muted">{t('create.kindHint')}</p>

	<label class="flex min-h-0 flex-1 flex-col gap-1.5">
		<span class="label">{t('create.contentLabel')}</span>
		<textarea
			bind:value={content}
			spellcheck="false"
			placeholder={t('create.contentPlaceholder')}
			class="input min-h-64 flex-1 resize-none font-mono text-xs sm:min-h-80"></textarea>
	</label>

	{#if error}
		<Alert>{error}</Alert>
	{/if}
</Modal>
