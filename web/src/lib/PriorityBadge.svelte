<script lang="ts">
	import type { Priority } from './api.ts';

	let { priority, heartbeat = false }: { priority: Priority | null; heartbeat?: boolean } = $props();

	const short: Record<Priority, string> = {
		Emergency: 'Emergency',
		'Non-emergency': 'Non-emerg.',
		Admin: 'Admin'
	};
</script>

{#if priority}
	<span class="badge {priority === 'Non-emergency' ? 'non-emergency' : priority.toLowerCase()}" title={priority}
		>{short[priority]}<span class="visually-hidden"> priority</span></span
	>
{/if}
{#if heartbeat}
	<span class="badge heartbeat" title="Network heartbeat">Heartbeat</span>
{/if}

<style>
	.badge {
		display: inline-block;
		padding: 0.05rem 0.4rem;
		border-radius: 0.25rem;
		font-size: 0.75rem;
		font-weight: 600;
		line-height: 1.4;
		white-space: nowrap;
		border: 1px solid transparent;
	}
	.emergency {
		background: var(--emergency-bg);
		color: var(--emergency-fg);
	}
	.non-emergency {
		background: var(--nonemergency-bg);
		color: var(--nonemergency-fg);
	}
	.admin {
		background: var(--admin-bg);
		color: var(--admin-fg);
	}
	.heartbeat {
		background: transparent;
		color: var(--muted);
		border-color: var(--border);
	}
</style>
