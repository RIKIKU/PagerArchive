<script lang="ts">
	// R34: every message of a job, ignored aliases' too, newest first, live,
	// within the search box's search (R33a).
	import { page } from '$app/state';
	import { api, areaName, type Meta } from '#lib/api.ts';
	import MessageList from '#lib/MessageList.svelte';
	import { criteria } from '#lib/search.ts';

	const job = $derived(page.params.job ?? '');
	const query = $derived(criteria(page.url));
	let count = $state(0);
	let more = $state(false);

	// R88b: the areas its pages are in, at a glance, asked again as pages
	// arrive (a new page may be in another).
	let meta: Meta | undefined = $state();
	let areas: string[] = $state([]);
	api.meta().then((m) => (meta = m)).catch(() => {});
	$effect(() => {
		void count;
		const asked = job;
		if (!meta?.areas) return;
		api
			.jobAreas(asked)
			.then((a) => {
				if (asked === job) areas = a.areas;
			})
			.catch(() => {});
	});
</script>

<svelte:head><title>Job {job} · PagerArchive</title></svelte:head>

<div class="job">
	<MessageList {query} base={{ job, ignored: 'true' }} title="Job {job}" bind:count bind:more>
		{#if meta?.areas && areas.length}
			<ul class="areas" aria-label="{meta.areas.label}, from where its pages were placed" data-testid="job-areas">
				{#each areas as a (a)}<li>{areaName(meta, a)}</li>{/each}
			</ul>
		{/if}
		<p class="status">
			{count} message{count === 1 ? '' : 's'}{more ? ' so far' : ''}, newest first. Pages are grouped by job number on a best-effort
			basis; the app doesn't track an incident's state.
		</p>
	</MessageList>
</div>

<style>
	/* The list runs edge to edge, as the message list does. */
	.job {
		margin: calc(-1 * var(--page-pad));
	}
	.status {
		margin: 0;
		padding: var(--space-sm) var(--row-pad-x);
		border-bottom: var(--border-width) solid var(--border);
	}
	.areas {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-xs) var(--space-sm);
		margin: 0;
		padding: var(--space-sm) var(--row-pad-x) 0;
		list-style: none;
	}
	.areas li {
		padding: var(--space-2xs) var(--space-sm);
		border: var(--border-width) solid var(--area-tag-border);
		border-radius: var(--radius-pill);
		background: var(--area-tag-bg);
		color: var(--fg);
		font-size: var(--text-md);
		font-weight: var(--weight-strong);
	}
	@media (max-width: 47.99rem) {
		.job {
			margin: calc(-1 * var(--page-pad-phone));
		}
	}
</style>
