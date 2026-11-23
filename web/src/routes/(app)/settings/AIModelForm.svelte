<script lang="ts">
	// The global "Ask AI" model: base URL, API key and model name. Super admin
	// only -- the page renders this for them alone, and the server refuses
	// everyone else. The stored key never comes back; the form shows whether one
	// is set and how it ends, and a blank key field keeps it.
	import Alert from '$lib/components/Alert.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import { errText } from '$lib/errors';
	import { updateAISettings, type AISettings } from '$lib/api/settings';

	let { initial, onsaved }: { initial: AISettings; onsaved: (s: AISettings) => void } = $props();

	// svelte-ignore state_referenced_locally
	let current = $state<AISettings>(initial);
	// DeepSeek is built in: choosing it fixes the base URL, so only the key and
	// the model are asked for. The server still just stores a base URL, so a
	// saved configuration pointing at DeepSeek reads back as that provider.
	const DEEPSEEK_URL = 'https://api.deepseek.com';
	const DEEPSEEK_MODELS = ['deepseek-chat', 'deepseek-reasoner'];
	type Provider = 'deepseek' | 'other';

	function providerOf(url: string): Provider {
		// A fresh install (no URL yet) starts on DeepSeek.
		return !url || url.replace(/\/+$/, '').replace(/\/v1$/, '') === DEEPSEEK_URL
			? 'deepseek'
			: 'other';
	}

	// svelte-ignore state_referenced_locally
	let provider = $state<Provider>(providerOf(initial.base_url));
	// svelte-ignore state_referenced_locally
	let baseUrl = $state(initial.base_url);
	// svelte-ignore state_referenced_locally
	let model = $state(
		initial.model || (providerOf(initial.base_url) === 'deepseek' ? DEEPSEEK_MODELS[0] : '')
	);
	let apiKey = $state('');
	let busy = $state(false);
	let status = $state<{ tone: 'ok' | 'bad'; text: string } | null>(null);

	async function save(patch: Parameters<typeof updateAISettings>[0]) {
		status = null;
		busy = true;
		try {
			current = await updateAISettings(patch);
			baseUrl = current.base_url;
			model = current.model;
			provider = providerOf(current.base_url);
			apiKey = '';
			status = { tone: 'ok', text: t('settings.aiSaved') };
			onsaved(current);
		} catch (err) {
			status = { tone: 'bad', text: errText(err, 'error.aiSettings') };
		} finally {
			busy = false;
		}
	}

	function submit(e: SubmitEvent) {
		e.preventDefault();
		const patch: Parameters<typeof updateAISettings>[0] = {
			base_url: provider === 'deepseek' ? DEEPSEEK_URL : baseUrl,
			model
		};
		if (apiKey.trim()) patch.api_key = apiKey;
		save(patch);
	}
</script>

<form onsubmit={submit} class="mt-4 flex flex-col gap-3">
	<div>
		<p class="label">{t('settings.aiModel')}</p>
		<p class="hint">{t('settings.aiModelHint')}</p>
	</div>

	<div class="field">
		<label for="ai-provider" class="label">{t('settings.aiProvider')}</label>
		<select
			id="ai-provider"
			bind:value={provider}
			class="input"
			onchange={() => {
				// Offer a sensible model when switching to DeepSeek; keep what was
				// typed otherwise.
				if (provider === 'deepseek' && !DEEPSEEK_MODELS.includes(model)) model = DEEPSEEK_MODELS[0];
			}}
		>
			<option value="deepseek">{t('settings.aiProviderDeepseek')}</option>
			<option value="other">{t('settings.aiProviderOther')}</option>
		</select>
		{#if provider === 'deepseek'}
			<p class="hint">{t('settings.aiProviderDeepseekHint')}</p>
		{/if}
	</div>

	{#if provider === 'other'}
		<div class="field">
			<label for="ai-base-url" class="label">{t('settings.aiBaseUrl')}</label>
			<input
				id="ai-base-url"
				bind:value={baseUrl}
				class="input font-mono"
				placeholder="https://api.openai.com/v1"
				autocomplete="off"
				spellcheck="false"
			/>
			<p class="hint">{t('settings.aiBaseUrlHint')}</p>
		</div>
	{/if}

	<div class="field">
		<label for="ai-api-key" class="label">{t('settings.aiApiKey')}</label>
		<input
			id="ai-api-key"
			type="password"
			bind:value={apiKey}
			class="input font-mono"
			placeholder={current.api_key_set ? '••••••••' : 'sk-…'}
			autocomplete="new-password"
			spellcheck="false"
		/>
		{#if current.api_key_set}
			<p class="hint flex flex-wrap items-center gap-x-2">
				{current.api_key_hint
					? t('settings.aiApiKeySet', { hint: current.api_key_hint })
					: t('settings.aiApiKeySetNoHint')}
				<button
					type="button"
					class="text-bad underline-offset-2 hover:underline"
					disabled={busy}
					onclick={() => save({ api_key: '' })}
				>
					{t('settings.aiApiKeyClear')}
				</button>
			</p>
		{/if}
	</div>

	<div class="field">
		<label for="ai-model" class="label">{t('settings.aiModelName')}</label>
		<input
			id="ai-model"
			bind:value={model}
			class="input font-mono"
			placeholder={t('settings.aiModelNamePlaceholder')}
			list={provider === 'deepseek' ? 'ai-model-options' : undefined}
			autocomplete="off"
			spellcheck="false"
		/>
		{#if provider === 'deepseek'}
			<datalist id="ai-model-options">
				{#each DEEPSEEK_MODELS as m (m)}
					<option value={m}></option>
				{/each}
			</datalist>
		{/if}
	</div>

	<button type="submit" disabled={busy} class="btn btn-soft self-start">
		{busy ? t('common.saving') : t('common.save')}
	</button>
	{#if status}
		<Alert tone={status.tone}>{status.text}</Alert>
	{/if}
</form>
