<script lang="ts">
	// R34: every message of a job, ignored aliases' too, newest first, live,
	// within the search box's search (R33a).
	import { page } from '$app/state';
	import MessageList from '#lib/MessageList.svelte';
	import { criteria } from '#lib/search.ts';

	const job = $derived(page.params.job ?? '');
	const query = $derived(criteria(page.url));
	let count = $state(0);
	let more = $state(false);
</script>

<svelte:head><title>Job {job} · PagerArchive</title></svelte:head>

<div class="job">
	<MessageList {query} base={{ job, ignored: 'true' }} title="Job {job}" bind:count bind:more>
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
	@media (max-width: 47.99rem) {
		.job {
			margin: calc(-1 * var(--page-pad-phone));
		}
	}
</style>
