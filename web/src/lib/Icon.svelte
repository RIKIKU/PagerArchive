<script lang="ts">
	// R77a: an icon never stands alone, so it's hidden from assistive
	// technology; the label or tooltip beside it names it. Icons are compiled
	// into the app (icons.ts), so nothing is fetched from anywhere.
	import { icons, type IconName } from './icons.ts';

	let { name }: { name: IconName } = $props();
</script>

<svg class="ui-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">{@html icons[name]}</svg>
