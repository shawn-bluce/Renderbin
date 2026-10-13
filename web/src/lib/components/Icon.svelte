<script lang="ts">
	// Inline Lucide icon. `icon` is written with the Lucide prefix so the lucideSubset
	// plugin (vite.config.ts) can find every name by scanning the source; the
	// SVG bodies come from that plugin's virtual module, not a runtime fetch.
	import icons from 'virtual:lucide';

	let {
		icon,
		width = 16,
		class: className = ''
	}: { icon: string; width?: number | string; class?: string } = $props();

	const body = $derived(icons[icon.replace(/^lucide:/, '')] ?? '');
</script>

<svg
	xmlns="http://www.w3.org/2000/svg"
	viewBox="0 0 24 24"
	{width}
	height={width}
	fill="none"
	stroke="currentColor"
	stroke-width="2"
	stroke-linecap="round"
	stroke-linejoin="round"
	class="shrink-0 {className}"
	aria-hidden="true"
>
	<!-- eslint-disable-next-line svelte/no-at-html-tags -- the bodies are Lucide's own SVG, bundled at build time; no runtime input reaches it -->
	{@html body}
</svg>
