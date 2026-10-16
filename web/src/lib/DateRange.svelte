<script lang="ts">
	// From and To dates for a GET form (search, map, reports). Each limits the
	// other (min and max), and a range that is still reversed (typed by hand,
	// say) stops the form from being sent, with a message saying why, so the
	// API never gets it.
	import { rangeProblem } from './filters.ts';

	let {
		from = '',
		to = '',
		required = false,
		fromLabel = 'From',
		toLabel = 'To'
	}: { from?: string; to?: string; required?: boolean; fromLabel?: string; toLabel?: string } = $props();

	const uid = $props.id();
	// Follow the values given (a new search, Clear), and what the viewer types.
	let start = $derived(from);
	let end = $derived(to);
	// The message shows once the viewer changes a date or tries to send the
	// form; a reversed range in the URL is reported by the page itself.
	let touched = $state(false);
	let fromInput: HTMLInputElement;
	let toInput: HTMLInputElement;
	const problem = $derived(rangeProblem(start, end));

	$effect(() => {
		fromInput.setCustomValidity(problem);
		toInput.setCustomValidity(problem);
	});
</script>

<label
	>{fromLabel}<input
		bind:this={fromInput}
		bind:value={start}
		name="from"
		type="date"
		{required}
		max={end || undefined}
		aria-describedby={problem ? `${uid}-range` : undefined}
		oninput={() => (touched = true)}
		oninvalid={() => (touched = true)}
	/></label
>
<label
	>{toLabel}<input
		bind:this={toInput}
		bind:value={end}
		name="to"
		type="date"
		{required}
		min={start || undefined}
		aria-describedby={problem ? `${uid}-range` : undefined}
		oninput={() => (touched = true)}
		oninvalid={() => (touched = true)}
	/></label
>
{#if problem && touched}
	<p class="range-error" id="{uid}-range" role="alert" data-testid="date-range-error">{problem}</p>
{/if}

<style>
	label {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
		font-size: var(--text-sm);
		min-width: 0;
	}
	/* A row of its own, in the search grid and the report form alike. */
	.range-error {
		grid-column: 1 / -1;
		flex-basis: 100%;
		order: 1; /* after the form's other fields and its button */
		margin: 0;
		color: var(--emergency-bg);
		font-size: var(--text-sm);
	}
</style>
