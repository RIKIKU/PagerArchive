<script lang="ts">
	import type { Priority } from '#lib/api.ts';
	import Icon from './Icon.svelte';

	let { priority, heartbeat = false }: { priority: Priority | null; heartbeat?: boolean } = $props();

	const cls: Record<Priority, string> = { Emergency: 'emergency', 'Non-emergency': 'nonemergency', Admin: 'admin' };
</script>

{#if priority}
	<span class="p-badge {cls[priority]}"
		><span class="dot" aria-hidden="true"></span>{priority}<span class="visually-hidden"> priority</span></span
	>
{/if}
{#if heartbeat}
	<span class="p-badge heartbeat"><Icon name="heartbeat" />Heartbeat</span>
{/if}
