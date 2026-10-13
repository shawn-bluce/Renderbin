<script lang="ts">
	// Simple on/off switch. Controlled: the parent owns the value and flips it
	// in onToggle (usually after an API call), so a failed save never leaves
	// the knob out of sync with the server.
	let {
		checked,
		disabled = false,
		label,
		onToggle
	}: {
		checked: boolean;
		disabled?: boolean;
		label: string;
		onToggle: () => void;
	} = $props();
</script>

<button
	type="button"
	role="switch"
	aria-checked={checked}
	aria-label={label}
	{disabled}
	onclick={onToggle}
	class="relative h-6 w-11 shrink-0 rounded-full border transition-colors disabled:cursor-not-allowed disabled:opacity-40
		{checked ? 'border-accent bg-accent' : 'border-line-strong bg-raised'}"
>
	<span
		class="absolute top-0.5 left-0.5 size-4.5 rounded-full transition-transform
			{checked ? 'translate-x-5 bg-on-accent' : 'bg-muted'}"
	></span>
</button>
