<script lang="ts">
	// The message list and a message's detail view (R80). From 1,280 px the
	// message opens in a pane beside the list, which keeps its search and
	// place; narrower, the message has the page to itself. Either way the
	// message has its own address (/messages/7), to bookmark and share.
	import { page } from '$app/state';
	import { list, listHref } from '#lib/listState.svelte.ts';
	import MessageList from '#lib/MessageList.svelte';
	import { screen } from '#lib/screen.svelte.ts';
	import { criteria } from '#lib/search.ts';

	let { children } = $props();
	let pane: HTMLElement | undefined = $state();

	const onList = $derived(page.route.id === '/(list)');
	const id = $derived(page.route.id === '/(list)/messages/[id]' ? Number(page.params.id) : undefined);

	// The list's search: the address's, on the list; while a message is open
	// beside it, the one it had (none, for a message opened from a bookmark).
	$effect(() => {
		if (onList) list.query = criteria(page.url).toString();
	});
	const query = $derived(onList ? criteria(page.url) : new URLSearchParams(list.query));
	const split = $derived(id !== undefined && screen.wide);

	// A new message in the pane starts at its top.
	$effect(() => {
		void id;
		pane?.scrollTo(0, 0);
	});
</script>

<div class="messages" class:split>
	{#if onList || split}
		<div class="list">
			<MessageList {query} remember selected={id} keepScroll={screen.wide} mapHref={list.query ? `/map?${list.query}` : '/map'} />
		</div>
	{/if}
	{#if id !== undefined}
		<section class="pane" class:beside={split} aria-label="Message" bind:this={pane}>
			{#if split}
				<div class="bar"><a class="button ghost" href={listHref()} data-sveltekit-reset="false">Close</a></div>
			{/if}
			{@render children()}
		</section>
	{:else}
		{@render children()}
	{/if}
</div>

<style>
	/* The list runs edge to edge: no centre column (R80). */
	.messages {
		margin: calc(-1 * var(--page-pad));
	}
	.split {
		display: grid;
		grid-template-columns: minmax(var(--list-min), 5fr) minmax(0, 6fr);
		align-items: start;
	}
	.split .list {
		border-right: var(--border-width) solid var(--border);
	}
	.pane {
		padding: var(--page-pad);
	}
	/* Beside the list, the pane stays put under the top bar and scrolls on
	   its own, so the list keeps its place. */
	.pane.beside {
		position: sticky;
		top: var(--topbar-height);
		max-height: calc(var(--full-height) - var(--topbar-height));
		overflow-y: auto;
		padding-top: var(--space-sm);
		background: var(--surface);
		container-type: inline-size;
	}
	.bar {
		display: flex;
		justify-content: flex-end;
	}
	@media (max-width: 47.99rem) {
		.messages {
			margin: calc(-1 * var(--page-pad-phone));
		}
		.pane {
			padding: var(--page-pad-phone);
		}
	}
</style>
