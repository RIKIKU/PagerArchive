<script lang="ts">
	// R39: admins see when a receiver has gone quiet, in the foot of the side
	// menu (ArchiveUx's Menu's foot, R77d), above Shrink menu. Shrunk, its
	// icon only, with a tooltip and an accessible name (R77a).
	import Icon from './Icon.svelte';

	let { quiet, shrunk }: { quiet: number; shrunk: boolean } = $props();
	const receivers = $derived(`${quiet} ${quiet === 1 ? 'receiver' : 'receivers'}`);
</script>

<div class="tipped" class:shrunk>
	<a class="item alert" href="/admin/clients" aria-label={shrunk ? `${receivers} not receiving` : undefined}
		><Icon name="warning" /><span class="label" data-testid="not-receiving">{receivers} not receiving</span></a
	>
	{#if shrunk}<span class="tip" aria-hidden="true">No pages from {quiet === 1 ? 'a receiver' : 'some receivers'} for 30 minutes</span>{/if}
</div>

<style>
	.item {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		width: 100%;
		min-height: var(--target);
		padding: 0 var(--space-sm);
		border-radius: var(--radius-button);
		font-size: var(--text-md);
		line-height: var(--leading-tight);
		text-decoration: none;
	}
	.alert {
		background: var(--warn-bg);
		color: var(--warn-fg);
		font-weight: var(--weight-strong);
	}
	.alert:hover {
		background: var(--warn-bg);
		text-decoration: underline;
	}
	.label {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.shrunk {
		position: relative;
		display: flex;
		justify-content: center;
	}
	.shrunk .label {
		display: none;
	}
	.shrunk .alert {
		position: relative;
		justify-content: center;
		width: var(--target-coarse);
		height: var(--target-coarse);
		padding: 0;
	}
	.tip {
		display: none;
		position: absolute;
		left: 100%;
		top: 50%;
		z-index: var(--layer-overlay);
		transform: translateY(-50%);
		padding: var(--space-2xs) var(--space-sm);
		border-radius: var(--radius-small);
		background: var(--fg);
		color: var(--bg);
		font-size: var(--text-xs);
		white-space: nowrap;
		pointer-events: none;
	}
	.shrunk:hover > .tip,
	.shrunk:focus-within > .tip {
		display: block;
	}
</style>
