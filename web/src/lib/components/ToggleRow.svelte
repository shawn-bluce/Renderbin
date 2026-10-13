<script lang="ts">
	// A setting: what it is, what it does, and the switch.
	import Toggle from './Toggle.svelte';

	let {
		label,
		hint,
		checked,
		disabled = false,
		onToggle,
		class: className = ''
	}: {
		label: string;
		hint: string;
		checked: boolean;
		disabled?: boolean;
		onToggle: () => void;
		class?: string;
	} = $props();
</script>

<div class="flex items-start justify-between gap-4 {className}">
	<div class="min-w-0">
		<p class="text-sm text-fg">{label}</p>
		<p class="hint">{hint}</p>
	</div>
	<Toggle {checked} {disabled} {label} {onToggle} />
</div>
