<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import Icon from '$lib/components/Icon.svelte';
	import { superForm, defaults } from 'sveltekit-superforms';
	import { zod4 as zod, zod4Client as zodClient } from 'sveltekit-superforms/adapters';
	import { registerSchema } from '$lib/schemas/register';
	import { setup } from '$lib/api/auth';
	import { t } from '$lib/i18n/index.svelte';
	import AuthLayout from '$lib/components/AuthLayout.svelte';
	import Alert from '$lib/components/Alert.svelte';
	import RegisterFields from '$lib/components/RegisterFields.svelte';
	import ToggleRow from '$lib/components/ToggleRow.svelte';

	let errorMessage = $state<string | null>(null);
	// Initial configs chosen during setup; plain toggles, not form fields.
	let allowRegistration = $state(false);
	let mcpEnabled = $state(false);

	const { form, errors, enhance, submitting } = superForm(defaults(zod(registerSchema)), {
		SPA: true,
		validators: zodClient(registerSchema),
		async onUpdate({ form }) {
			if (!form.valid) return;
			errorMessage = null;
			try {
				await setup({
					username: form.data.username,
					nickname: form.data.nickname,
					password: form.data.password,
					allow_registration: allowRegistration,
					mcp_enabled: mcpEnabled
				});
				await goto(resolve('/'));
			} catch {
				errorMessage = t('welcome.failed');
			}
		}
	});
</script>

<svelte:head><title>{t('welcome.title')} · Renderbin</title></svelte:head>

<AuthLayout wide>
	<h1 class="text-lg font-semibold text-fg-strong">{t('welcome.title')}</h1>
	<p class="mt-1 mb-6 text-sm text-muted">{t('welcome.subtitle')}</p>

	<form method="POST" use:enhance class="flex flex-col gap-4">
		<div class="grid gap-4 sm:grid-cols-2">
			<RegisterFields {form} {errors} />
		</div>

		<div class="panel flex flex-col gap-4 p-4">
			<p class="label">{t('welcome.options')}</p>
			<ToggleRow
				label={t('config.allowRegistration')}
				hint={t('config.allowRegistrationHint')}
				checked={allowRegistration}
				onToggle={() => (allowRegistration = !allowRegistration)}
			/>
			<ToggleRow
				label={t('config.enableMcp')}
				hint={t('config.enableMcpHint')}
				checked={mcpEnabled}
				onToggle={() => (mcpEnabled = !mcpEnabled)}
			/>
		</div>

		<button type="submit" disabled={$submitting} class="btn btn-primary mt-1 h-10">
			<Icon icon="lucide:rocket" width="16" />
			{$submitting ? t('welcome.submitting') : t('welcome.submit')}
		</button>
	</form>

	{#if errorMessage}
		<Alert class="mt-4">{errorMessage}</Alert>
	{/if}
</AuthLayout>
