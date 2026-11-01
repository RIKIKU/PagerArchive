<script lang="ts">
	// The Reports group's start page. Before M13 this address was the
	// capcode report, which is now /reports/capcode: a link to a report
	// (?capcode=…) goes there.
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import GroupPage from '#lib/GroupPage.svelte';
	import { groups } from '#lib/nav.ts';

	if (page.url.search) goto(`/reports/capcode${page.url.search}`, { replaceState: true });
</script>

{#if !page.url.search}<GroupPage group={groups.find((g) => g.id === 'reports')!} />{/if}
