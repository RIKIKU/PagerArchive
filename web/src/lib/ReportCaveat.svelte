<script lang="ts">
	// R38a (owner, 2026-11-15): every report says its numbers can be wrong,
	// as they count only what PagerArchive received and stored.
	import Icon from './Icon.svelte';
</script>

<p class="caveat" data-testid="report-caveat">
	<Icon name="warning" />
	<span>These numbers can be wrong: they count only the pages PagerArchive received and stored. Pages missed by the radio, or lost while it was down, aren't counted.</span>
</p>

<style>
	.caveat {
		display: flex;
		align-items: flex-start;
		gap: var(--space-xs);
		max-width: var(--measure);
		margin: var(--space-xs) 0;
		padding: var(--space-xs) var(--space-sm);
		border-radius: var(--radius-small);
		background: var(--warn-bg);
		color: var(--warn-fg);
		font-size: var(--text-sm);
	}
	.caveat :global(.ui-icon) {
		flex: none;
		width: var(--icon-size);
		height: var(--icon-size);
	}
</style>
