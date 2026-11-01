<script lang="ts">
	// R44b: the Administration start page links to every admin screen.
	import { api, type ClientStatus } from '#lib/api.ts';
	import GroupPage from '#lib/GroupPage.svelte';
	import { groups } from '#lib/nav.ts';
	import { refreshWaiting, waiting } from '#lib/waiting.svelte.ts';

	let quiet = $state(0);
	api
		.clients()
		.then((cs: ClientStatus[]) => (quiet = cs.filter((c) => !c.receiving).length))
		.catch(() => {});
	refreshWaiting();
</script>

<GroupPage group={groups.find((g) => g.id === 'admin')!} testid="admin-screens">
	{#snippet extra(e)}
		{#if e.href === '/admin/clients' && quiet > 0}<span class="tag">{quiet} not receiving</span>{/if}
		{#if e.href === '/admin/requests' && waiting.count > 0}<span class="tag" data-testid="waiting-tag">{waiting.count} waiting</span>{/if}
	{/snippet}
</GroupPage>

<style>
	.tag {
		margin-left: var(--space-sm);
		padding: 0 var(--space-sm);
		border-radius: var(--radius-pill);
		background: var(--warn-bg);
		color: var(--warn-fg);
		font-size: var(--text-xs);
		font-weight: var(--weight-strong);
	}
</style>
