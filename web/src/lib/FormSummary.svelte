<script lang="ts">
	// R82e: the line at the top of a form after a save with problems. It only
	// counts them ("2 fields need attention"); each is under its own field.
	import type { FormCheck } from './formCheck.svelte.ts';

	let { check }: { check: FormCheck } = $props();
</script>

<div aria-live="polite">
	{#if check.summary}<p class="error" data-testid="form-summary">{check.summary}</p>{/if}
</div>
