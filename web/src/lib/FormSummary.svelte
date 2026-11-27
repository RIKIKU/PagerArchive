<script lang="ts">
	// R82g: the error summary at the top of a form after a save with problems
	// (ArchiveUx's ErrorSummary, its forms rule 4, 0.6.0): a title counting
	// them ("2 fields need attention"), then a link for each, naming its field
	// and saying what to do, which moves focus to that field. Each problem is
	// under its own field too. After such a save the focus comes here.
	// level is the title's heading: one below the form's own heading.
	import { ErrorSummary } from 'archiveux/components';
	import type { FormCheck } from './formCheck.svelte.ts';

	let { check, level = 2 }: { check: FormCheck; level?: 2 | 3 | 4 } = $props();

	let summary: { focus(): void } | undefined = $state();

	$effect(() => {
		check.summaryFocus(() => summary?.focus());
		return () => check.summaryFocus(undefined);
	});
</script>

<div data-testid="form-summary">
	<ErrorSummary bind:this={summary} problems={check.listed} {level} />
</div>
