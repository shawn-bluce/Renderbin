<script lang="ts">
	// Unknown URLs land here (signed in or not). The signed-in pages live in the
	// (app) group, so a stray path no longer passes through their auth guard.
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import AuthLayout from '$lib/components/AuthLayout.svelte';
	import { t } from '$lib/i18n/index.svelte';
</script>

<AuthLayout>
	<p class="font-mono text-3xl font-bold text-accent">{page.status}</p>
	<p class="mt-2 text-sm text-muted">
		{page.status === 404 ? t('error.pageNotFound') : page.error?.message}
	</p>
	<a href={resolve('/')} class="btn btn-primary mt-6 w-full">{t('error.backHome')}</a>
</AuthLayout>
