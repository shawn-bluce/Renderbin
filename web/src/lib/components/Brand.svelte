<script lang="ts">
	// The mark is the favicon, inlined so the sidebar/auth pages need no request.
	let { size = 28 }: { size?: number } = $props();
</script>

<span class="inline-flex items-center gap-2.5">
	<svg width={size} height={size} viewBox="0 0 32 32" aria-hidden="true">
		<rect width="32" height="32" rx="7" fill="var(--accent)" />
		<path
			d="M12 10l-6 6 6 6M20 10l6 6-6 6"
			fill="none"
			stroke="var(--on-accent)"
			stroke-width="3"
			stroke-linecap="round"
			stroke-linejoin="round"
		/>
	</svg>
	<span class="text-base font-bold tracking-tight text-fg-strong">Renderbin</span>
</span>
