<script lang="ts">
	// The Dates below are ephemeral arithmetic locals, never stored in $state.
	/* eslint-disable svelte/prefer-svelte-reactivity */
	// Expiry modal. A ttl is an amount plus a unit ("1d", "36h", "6mo"), the same
	// grammar the API speaks, so the picker is free-form rather than a preset list.
	import Alert from '$lib/components/Alert.svelte';
	import Modal from '$lib/components/Modal.svelte';
	import { fullDate } from '$lib/dates';
	import { errText } from '$lib/errors';
	import { t } from '$lib/i18n/index.svelte';
	import type { MessageKey } from '$lib/i18n/messages';
	import { setExpiry, type FileItem } from '$lib/api/files';

	let {
		file,
		onclose,
		onsaved
	}: { file: FileItem; onclose: () => void; onsaved: (file: FileItem) => void } = $props();

	const units = [
		{ key: 'h', labelKey: 'ttl.hours' },
		{ key: 'd', labelKey: 'ttl.days' },
		{ key: 'w', labelKey: 'ttl.weeks' },
		{ key: 'mo', labelKey: 'ttl.months' },
		{ key: 'y', labelKey: 'ttl.years' }
	] as const satisfies { key: string; labelKey: MessageKey }[];
	type Unit = (typeof units)[number]['key'];

	const modes = [
		{ key: 'none', labelKey: 'expiry.unlimited' },
		{ key: 'time', labelKey: 'expiry.timeLimit' },
		{ key: 'views', labelKey: 'expiry.viewLimit' }
	] as const satisfies { key: string; labelKey: MessageKey }[];

	// Mirrors handlers.maxTTLYears -- the client-side check exists only to give a
	// better message than the server's 400; the server still decides.
	const MAX_TTL_YEARS = 10;

	// Only the deadline is stored, not the amount/unit it came from, so an
	// existing time limit reopens on the default amount rather than a guess.
	// svelte-ignore state_referenced_locally
	let mode = $state<'none' | 'time' | 'views'>(
		file.expires_at ? 'time' : file.max_views != null ? 'views' : 'none'
	);
	let ttlValue = $state(1);
	let ttlUnit = $state<Unit>('d');
	// svelte-ignore state_referenced_locally
	let views = $state(file.max_views ?? 10);
	let busy = $state(false);
	let error = $state<string | null>(null);

	// Stamped once so the preview is computed from one fixed "now" rather than
	// drifting between keystrokes.
	const now = Date.now();

	// The deadline the amount/unit would produce, for the preview line. Calendar
	// units step the calendar (a month later is the same day of the next month),
	// matching the server's AddDate arithmetic. null = the server would reject it.
	function deadline(value: number, unit: Unit): Date | null {
		if (!Number.isInteger(value) || value <= 0) return null;
		const d = new Date(now);
		if (unit === 'h') d.setHours(d.getHours() + value);
		else if (unit === 'd') d.setDate(d.getDate() + value);
		else if (unit === 'w') d.setDate(d.getDate() + value * 7);
		else if (unit === 'mo') d.setMonth(d.getMonth() + value);
		else d.setFullYear(d.getFullYear() + value);
		const cap = new Date(now);
		cap.setFullYear(cap.getFullYear() + MAX_TTL_YEARS);
		return d > cap ? null : d;
	}

	const preview = $derived(mode === 'time' ? deadline(ttlValue, ttlUnit) : null);

	async function save() {
		let payload: { ttl?: string | null; max_views?: number | null };
		if (mode === 'time') {
			if (!deadline(ttlValue, ttlUnit)) {
				error = t('error.ttlValue', { years: MAX_TTL_YEARS });
				return;
			}
			payload = { ttl: `${ttlValue}${ttlUnit}` };
		} else if (mode === 'views') {
			if (!Number.isInteger(views) || views <= 0) {
				error = t('error.viewCount');
				return;
			}
			payload = { max_views: views };
		} else {
			payload = {};
		}
		error = null;
		busy = true;
		try {
			onsaved(await setExpiry(file.slug, payload));
		} catch (err) {
			error = errText(err, 'error.save');
			busy = false;
		}
	}
</script>

<Modal title={t('expiry.title')} {onclose} {busy} onsubmit={save}>
	<p class="text-xs text-muted">{t('expiry.note')}</p>

	<div role="group" aria-label={t('expiry.title')} class="seg w-full *:flex-1">
		{#each modes as opt (opt.key)}
			<button onclick={() => (mode = opt.key)} aria-pressed={mode === opt.key}>
				{t(opt.labelKey)}
			</button>
		{/each}
	</div>

	{#if mode === 'time'}
		<div class="flex flex-col gap-2">
			<span class="label">{t('expiry.expiresAfter')}</span>
			<div class="flex flex-wrap items-center gap-2">
				<input type="number" min="1" step="1" bind:value={ttlValue} class="input w-20" />
				<div role="group" aria-label={t('expiry.expiresAfter')} class="seg">
					{#each units as unit (unit.key)}
						<button onclick={() => (ttlUnit = unit.key)} aria-pressed={ttlUnit === unit.key}>
							{t(unit.labelKey)}
						</button>
					{/each}
				</div>
			</div>
			<!-- The deadline in plain words: "1 month" is easy to pick and hard to
			     picture, and this is the number the owner has to plan around. -->
			<p class="text-xs text-muted">
				{#if preview}
					{t('expiry.preview', { datetime: fullDate(preview) })}
				{:else}
					{t('error.ttlValue', { years: MAX_TTL_YEARS })}
				{/if}
			</p>
		</div>
	{:else if mode === 'views'}
		<label class="field">
			<span class="label">{t('expiry.maxViews')}</span>
			<input type="number" min="1" bind:value={views} class="input w-32" />
		</label>
	{:else}
		<p class="text-xs text-muted">{t('expiry.noExpiry')}</p>
	{/if}

	{#if error}
		<Alert>{error}</Alert>
	{/if}
</Modal>
