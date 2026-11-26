<script lang="ts">
	// A message's priority in words, never colour alone (WCAG 1.4.1), and its
	// heartbeat badge (R35b). How each priority looks is in its tokens (R82c).
	import type { Priority } from './api.ts';
	import { StateBadge } from 'archiveux/components';
	import Icon from './Icon.svelte';

	let {
		priority,
		heartbeat = false,
		noise = false
	}: { priority: Priority | null; heartbeat?: boolean; noise?: boolean } = $props();

	// R35h: why a page is probably noise, as its tooltip and in words for a
	// screen reader. Nothing else treats the page differently. It's ArchiveUx's
	// neutral state badge (a hint, not an alarm), as RadioArchive shows states;
	// StateBadge takes no other attributes, so the tooltip and the tests' hook
	// (data-noise) sit on a span around it.
	const noiseWhy = 'Heard by one receiver only, matched no format, capcode never seen before';

	const cls: Record<Priority, string> = { Emergency: 'emergency', 'Non-emergency': 'non-emergency', Admin: 'admin' };
</script>

{#if priority}
	<span class="badge {cls[priority]}"
		><span class="dot" aria-hidden="true"></span>{priority} <span class="visually-hidden">priority</span></span
	>
{/if}
{#if heartbeat}
	<span class="badge heartbeat" title="Network heartbeat"><Icon name="heartbeat" />Heartbeat</span>
{/if}
{#if noise}
	<span class="noise" title={noiseWhy} data-noise
		><StateBadge state="neutral">Probably noise<span class="visually-hidden">: {noiseWhy}</span></StateBadge></span
	>
{/if}

<style>
	.badge {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		padding: var(--badge-pad-y) var(--badge-pad-x);
		border: var(--border-width) solid transparent;
		border-radius: var(--radius-badge);
		font-family: var(--font-meta);
		font-size: var(--text-xs);
		font-weight: var(--weight-strong);
		letter-spacing: var(--badge-tracking);
		line-height: var(--leading);
		text-transform: var(--badge-case);
		white-space: nowrap;
	}
	.dot {
		display: var(--badge-dot);
		flex: none;
		width: var(--badge-dot-size);
		height: var(--badge-dot-size);
		border-radius: var(--radius-round);
	}
	.emergency {
		background: var(--emergency-badge-bg);
		border-color: var(--emergency-badge-border);
		color: var(--emergency-badge-fg);
	}
	.emergency .dot {
		background: var(--emergency-dot);
	}
	.non-emergency {
		background: var(--nonemergency-badge-bg);
		border-color: var(--nonemergency-badge-border);
		color: var(--nonemergency-badge-fg);
	}
	.non-emergency .dot {
		background: var(--nonemergency-dot);
	}
	.admin {
		background: var(--admin-badge-bg);
		border-color: var(--admin-badge-border);
		color: var(--admin-badge-fg);
	}
	.admin .dot {
		background: var(--admin-dot);
	}
	.heartbeat {
		border-color: var(--heartbeat-border);
		color: var(--heartbeat-fg);
	}
	/* R35h: the span around the badge takes the badge's own box, so it lines
	   up as the others do. */
	.noise {
		display: inline-flex;
	}
</style>
