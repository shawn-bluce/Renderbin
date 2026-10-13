<script lang="ts">
	import './layout.css';
	import favicon from '$lib/assets/favicon.svg';
	import { initI18n } from '$lib/i18n/index.svelte';
	import { initTheme } from '$lib/theme.svelte';

	// Restore the persisted / preferred locale and theme once, before first paint.
	// Safe to run synchronously here: the app is a pure SPA (ssr = false).
	initI18n();
	initTheme();

	let { children } = $props();
</script>

<svelte:head>
	<!-- App-wide default; a page can override it with its own <title>. -->
	<title>Renderbin</title>
	<link rel="icon" href={favicon} />
</svelte:head>
{@render children()}
