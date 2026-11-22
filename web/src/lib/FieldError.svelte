<script lang="ts">
	// R82g: a control's or group's problem, under it (ArchiveUx .field-error).
	// The control points at it: aria-describedby={check.describedBy(id)}.
	import { errorId, type FormCheck } from './formCheck.svelte.ts';

	let { check, id }: { check: FormCheck; id: string } = $props();
</script>

{#if check.problems[id]}<p class="field-error" id={errorId(id)}>{check.problems[id]}</p>{/if}
