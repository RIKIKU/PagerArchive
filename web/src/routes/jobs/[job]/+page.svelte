<script lang="ts">
	// R34: every message of a job, ignored aliases' too, newest first, live,
	// within the search box's search (R33a).
	import { page } from '$app/state';
	import { api, type AreaRef, type Meta } from '#lib/api.ts';
	import AreaTags from '#lib/AreaTags.svelte';
	import MessageList from '#lib/MessageList.svelte';
	import RadioChips from '#lib/RadioChips.svelte';
	import { criteria } from '#lib/search.ts';

	const job = $derived(page.params.job ?? '');
	const query = $derived(criteria(page.url));
	let count = $state(0);
	let more = $state(false);

	// R88b: the areas its pages are in, at a glance, asked again as pages
	// arrive (a new page may be in another).
	let meta: Meta | undefined = $state();
	let areas: AreaRef[] = $state([]);
	api.meta().then((m) => (meta = m)).catch(() => {});
	$effect(() => {
		void count;
		const asked = job;
		if (!meta?.areas?.length) return;
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
		{#if meta?.areas?.length && areas.length}
			<div class="areas">
				<AreaTags {meta} {areas} label="Areas, from where its pages were placed" testid="job-areas" />
				<!-- R92f: the dispatch channel of each of its areas that has one, linking to the radio. -->
				<RadioChips dispatch={areas.flatMap((a) => (a.dispatch_channel != null ? [a.dispatch_channel] : []))} testid="job-channels" />
			</div>
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
		padding: var(--space-sm) var(--row-pad-x) 0;
	}
	@media (max-width: 47.99rem) {
		.job {
			margin: calc(-1 * var(--page-pad-phone));
		}
	}
</style>
