<script lang="ts">
	import { onSheet, sheet, tick } from './sheet.svelte.ts';

	let { id }: { id: number } = $props();

	// R67: ticking adds the message to the print sheet, unticking takes it
	// off. A refused tick (the sheet is full) is undone; the print page says
	// why.
	function ticked(e: Event & { currentTarget: HTMLInputElement }) {
		if (!tick(id, e.currentTarget.checked)) e.currentTarget.checked = onSheet(id);
	}
</script>

<label class="tick">
	<input type="checkbox" aria-label="Add to print sheet" checked={onSheet(id)} disabled={!sheet.ready} onchange={ticked} />
	<span aria-hidden="true">Print</span>
</label>

<style>
	/* Above the row's stretched link, at the row's far end, away from the
	   date that a tap on the row's corner lands on (A22). */
	.tick {
		position: relative;
		z-index: var(--layer-content);
		margin-left: auto;
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		color: var(--muted);
		font-size: var(--text-xs);
		cursor: pointer;
		/* A bigger target than the box alone. */
		padding: 0.2rem 0 0.2rem 0.4rem;
		margin-block: -0.2rem;
	}
</style>
