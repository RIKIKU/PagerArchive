<script lang="ts">
	import { page } from '$app/state';
	import { criteria } from '#lib/filters.ts';
	import MessageList from '#lib/MessageList.svelte';

	const query = $derived(criteria(page.url));
</script>

<svelte:head><title>Messages · PagerArchive</title></svelte:head>

<MessageList {query} />
