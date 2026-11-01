<script lang="ts">
	// R77a: an icon never stands alone, so it's always hidden from assistive
	// technology; its label or tooltip beside it names it.
	import { icons, type IconName } from './icons.ts';

	let { name }: { name: IconName } = $props();
</script>

<svg class="p-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">{@html icons[name]}</svg>
