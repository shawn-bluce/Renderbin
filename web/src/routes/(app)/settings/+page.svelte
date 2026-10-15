<script lang="ts">
	import { resolve } from '$app/paths';
	import Alert from '$lib/components/Alert.svelte';
	import CopyButton from '$lib/components/CopyButton.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import Modal from '$lib/components/Modal.svelte';
	import SectionTitle from '$lib/components/SectionTitle.svelte';
	import Shell from '$lib/components/Shell.svelte';
	import ToggleRow from '$lib/components/ToggleRow.svelte';
	import { t } from '$lib/i18n/index.svelte';
	import AccountsSection from './AccountsSection.svelte';
	import BackupSection from './BackupSection.svelte';
	import { ApiError } from '$lib/api/http';
	import { ensureApiKey, resetApiKey, updateProfile, updateSettings } from '$lib/api/settings';
	import type { Settings } from '$lib/api/settings';
	import { changePasswordSchema } from '$lib/schemas/register';
	import type { MessageKey } from '$lib/i18n/messages';

	let { data } = $props();

	const isAdmin = $derived(data.user.is_admin);

	type Status = { tone: 'ok' | 'bad'; text: string };

	// --- User section ---
	// Intentional one-time snapshots (here and in the config section below):
	// these are edited locally / synced from API responses and shouldn't
	// re-sync if `data` changes.
	// svelte-ignore state_referenced_locally
	let nickname = $state(data.user.nickname);
	let nicknameBusy = $state(false);
	let nicknameStatus = $state<Status | null>(null);

	let currentPassword = $state('');
	let newPassword = $state('');
	let rePassword = $state('');
	let passwordBusy = $state(false);
	let passwordStatus = $state<Status | null>(null);

	// --- Config sections ---
	// svelte-ignore state_referenced_locally
	let settings = $state<Settings>(data.settings);
	// svelte-ignore state_referenced_locally
	let apiKey = $state<string | null>(data.apiKey);
	let configError = $state<string | null>(null);

	// Ready-to-paste prompt that walks an AI agent through installing this MCP
	// server. Built through t(), so it re-renders in the active locale.
	const setupPrompt = $derived(
		apiKey ? t('settings.setupPromptText', { endpoint: `${location.origin}/mcp`, apiKey }) : ''
	);

	// --- Reset confirmation modal ---
	let resetOpen = $state(false);
	let resetBusy = $state(false);

	async function saveNickname() {
		nicknameStatus = null;
		const trimmed = nickname.trim();
		if (!trimmed) {
			nicknameStatus = { tone: 'bad', text: t('register.nicknameRequired') };
			return;
		}
		nicknameBusy = true;
		try {
			await updateProfile({ nickname: trimmed });
			nicknameStatus = { tone: 'ok', text: t('settings.nicknameUpdated') };
		} catch {
			nicknameStatus = { tone: 'bad', text: t('error.save') };
		} finally {
			nicknameBusy = false;
		}
	}

	async function savePassword() {
		passwordStatus = null;
		const parsed = changePasswordSchema.safeParse({
			currentPassword,
			password: newPassword,
			rePassword
		});
		if (!parsed.success) {
			passwordStatus = { tone: 'bad', text: t(parsed.error.issues[0].message as MessageKey) };
			return;
		}
		passwordBusy = true;
		try {
			await updateProfile({ current_password: currentPassword, new_password: newPassword });
			passwordStatus = { tone: 'ok', text: t('settings.passwordUpdated') };
			currentPassword = '';
			newPassword = '';
			rePassword = '';
		} catch (err) {
			passwordStatus = {
				tone: 'bad',
				text:
					err instanceof ApiError && err.status === 403 ? t('error.wrongPassword') : t('error.save')
			};
		} finally {
			passwordBusy = false;
		}
	}

	// Flips one boolean setting. The switch moves only once the server has
	// accepted it, to whatever the server says the value now is.
	async function saveSetting(key: keyof Settings) {
		configError = null;
		try {
			settings = await updateSettings({ [key]: !settings[key] });
		} catch {
			configError = t('error.updateSettings');
			return;
		}
		if (key === 'mcp_enabled' && settings.mcp_enabled && !apiKey) {
			// Enabling MCP issues the user an API key (reused if one exists).
			try {
				apiKey = await ensureApiKey();
			} catch {
				configError = t('error.apiKey');
			}
		}
	}

	async function confirmReset() {
		configError = null;
		resetBusy = true;
		try {
			apiKey = await resetApiKey();
			resetOpen = false;
		} catch {
			configError = t('error.apiKey');
		} finally {
			resetBusy = false;
		}
	}
</script>

{#snippet nav(mobile: boolean)}
	{@const items = [
		{ id: 'profile', icon: 'lucide:user', label: t('settings.user'), show: true },
		{ id: 'accounts', icon: 'lucide:users', label: t('accounts.title'), show: isAdmin },
		{ id: 'backup', icon: 'lucide:database-backup', label: t('backup.title'), show: isAdmin },
		{ id: 'registration', icon: 'lucide:user-plus', label: t('settings.registration'), show: true },
		{ id: 'sharing', icon: 'lucide:share-2', label: t('settings.sharing'), show: true },
		{ id: 'ai', icon: 'lucide:sparkles', label: t('settings.ai'), show: true }
	]}
	{@const itemClass = mobile ? 'nav-chip' : 'nav-item'}
	<ul class={mobile ? 'flex gap-1.5' : 'flex flex-col gap-0.5'}>
		<li class={mobile ? 'shrink-0' : ''}>
			<a href={resolve('/')} class={itemClass}>
				<Icon icon="lucide:arrow-left" width="15" />
				{t('settings.back')}
			</a>
		</li>
		{#each items.filter((i) => i.show) as item (item.id)}
			<li class={mobile ? 'shrink-0' : ''}>
				<!-- eslint-disable-next-line svelte/no-navigation-without-resolve -- in-page anchor -->
				<a href="#{item.id}" class={itemClass}>
					<Icon icon={item.icon} width="15" />
					{item.label}
				</a>
			</li>
		{/each}
	</ul>
{/snippet}

<!-- A setting everyone can see; only the super admin can change it. -->
{#snippet setting(label: string, hint: string, key: keyof Settings)}
	<ToggleRow
		class="mt-4"
		{label}
		hint={isAdmin ? hint : `${hint} ${t('settings.adminOnly')}`}
		checked={settings[key]}
		disabled={!isAdmin}
		onToggle={() => saveSetting(key)}
	/>
{/snippet}

<Shell user={data.user} {nav}>
	<main class="mx-auto flex w-full max-w-5xl flex-col gap-5 px-4 py-6 sm:px-8">
		<h1 class="text-xl font-semibold tracking-tight text-fg-strong">{t('settings.title')}</h1>

		<!-- Section: user -->
		<div id="profile" class="grid scroll-mt-20 items-start gap-5 xl:grid-cols-2">
			<section class="card p-5">
				<SectionTitle icon="lucide:user" label={t('settings.user')} />

				<div class="field mt-4">
					<label for="nickname" class="label">{t('settings.nickname')}</label>
					<div class="flex gap-2">
						<input id="nickname" bind:value={nickname} class="input min-w-0 flex-1" />
						<button onclick={saveNickname} disabled={nicknameBusy} class="btn btn-soft shrink-0">
							{nicknameBusy ? t('common.saving') : t('common.save')}
						</button>
					</div>
					{#if nicknameStatus}
						<Alert tone={nicknameStatus.tone} class="mt-1">{nicknameStatus.text}</Alert>
					{/if}
				</div>
			</section>

			<section class="card p-5">
				<SectionTitle icon="lucide:key-round" label={t('settings.changePassword')} />
				<div class="mt-4 flex flex-col gap-2">
					<input
						type="password"
						bind:value={currentPassword}
						placeholder={t('settings.currentPassword')}
						aria-label={t('settings.currentPassword')}
						autocomplete="current-password"
						class="input"
					/>
					<input
						type="password"
						bind:value={newPassword}
						placeholder={t('settings.newPassword')}
						aria-label={t('settings.newPassword')}
						autocomplete="new-password"
						class="input"
					/>
					<input
						type="password"
						bind:value={rePassword}
						placeholder={t('register.rePassword')}
						aria-label={t('register.rePassword')}
						autocomplete="new-password"
						class="input"
					/>
					<button onclick={savePassword} disabled={passwordBusy} class="btn btn-soft self-start">
						{passwordBusy ? t('common.saving') : t('common.save')}
					</button>
					{#if passwordStatus}
						<Alert tone={passwordStatus.tone}>{passwordStatus.text}</Alert>
					{/if}
				</div>
			</section>
		</div>

		<!-- Sections: accounts and backup/restore. Both are rendered only for the
		     super admin, unlike the config toggles below, which everyone sees
		     (disabled) since knowing the instance's policy is useful. `users` is
		     null for anyone else, so this can't render a list it doesn't have. -->
		{#if isAdmin && data.users}
			<AccountsSection users={data.users} />
		{/if}
		{#if isAdmin}
			<BackupSection />
		{/if}

		<div class="grid items-start gap-5 xl:grid-cols-2">
			<!-- Section: registration -->
			<section id="registration" class="card scroll-mt-20 p-5">
				<SectionTitle icon="lucide:user-plus" label={t('settings.registration')} />
				{@render setting(
					t('config.allowRegistration'),
					t('config.allowRegistrationHint'),
					'allow_registration'
				)}
			</section>

			<!-- Section: sharing -->
			<section id="sharing" class="card scroll-mt-20 p-5">
				<SectionTitle icon="lucide:share-2" label={t('settings.sharing')} />
				{@render setting(
					t('config.uploadDefaultPublic'),
					t('config.uploadDefaultPublicHint'),
					'upload_default_public'
				)}
				{@render setting(t('config.qrWidget'), t('config.qrWidgetHint'), 'qr_widget')}
			</section>
		</div>

		<!-- Section: AI capability -->
		<section id="ai" class="card scroll-mt-20 p-5">
			<SectionTitle icon="lucide:sparkles" label={t('settings.ai')} />
			{@render setting(t('config.enableMcp'), t('config.enableMcpHint'), 'mcp_enabled')}

			{#if settings.mcp_enabled && apiKey}
				<div class="mt-4 border-t border-line pt-4">
					<p class="label">{t('settings.apiKey')}</p>
					<p class="hint">
						{t('settings.apiKeyHint', { endpoint: `${location.origin}/mcp` })}
					</p>
					<div class="mt-2 flex flex-wrap items-center gap-2">
						<code class="panel min-w-0 px-3 py-2 font-mono text-xs break-all text-fg">
							{apiKey}
						</code>
						<CopyButton text={apiKey} labeled />
						<button
							onclick={() => (resetOpen = true)}
							class="btn btn-ghost btn-sm text-bad hover-bad"
						>
							<Icon icon="lucide:rotate-ccw" width="14" />
							{t('settings.resetApiKey')}
						</button>
					</div>
				</div>

				<div class="mt-4 border-t border-line pt-4">
					<div class="flex items-start justify-between gap-4">
						<div class="min-w-0">
							<p class="label">{t('settings.setupPrompt')}</p>
							<p class="hint">{t('settings.setupPromptHint')}</p>
						</div>
						<CopyButton text={setupPrompt} labeled class="btn btn-ghost btn-sm shrink-0" />
					</div>
					<pre
						class="panel mt-2 max-h-72 overflow-auto p-3 font-mono text-xs leading-relaxed break-words whitespace-pre-wrap text-fg">{setupPrompt}</pre>
				</div>
			{/if}

			{#if configError}
				<Alert class="mt-4">{configError}</Alert>
			{/if}
		</section>
	</main>
</Shell>

{#if resetOpen}
	<Modal
		title={t('settings.resetConfirmTitle')}
		onclose={() => (resetOpen = false)}
		busy={resetBusy}
		onsubmit={confirmReset}
		tone="danger"
		submitIcon="lucide:rotate-ccw"
		submitLabel={t('settings.resetConfirm')}
		busyLabel={t('settings.resetting')}
	>
		<p class="text-sm text-muted">{t('settings.resetConfirmBody')}</p>
	</Modal>
{/if}
