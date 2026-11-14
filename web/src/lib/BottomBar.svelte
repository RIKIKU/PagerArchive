<script lang="ts">
	// R81: phones get a bottom bar with Messages, Map, Albums and More; More
	// opens the rest of the menu, without the pages that are desktop only (the
	// print sheet, Administration). They still open from a link or bookmark.
	// At the bottom of More, the build's version, as at the foot of the side
	// menu (owner, 2026-11-14).
	import Icon from './Icon.svelte';
	import { entryOf, visible } from './nav.ts';

	let { path, admin, listHref, mapHref, version }: { path: string; admin: boolean; listHref: string; mapHref: string; version: string } = $props();

	let dialog: HTMLDialogElement;
	let more = $state(false);
	const groups = $derived(
		visible(admin)
			.filter((g) => !g.adminOnly && g.id !== 'albums')
			.map((g) => ({ ...g, entries: g.entries.filter((e) => !e.desktopOnly) }))
			.filter((g) => g.entries.length > 0)
	);
	const onAlbums = $derived(path === '/albums' || path.startsWith('/albums/'));
	const inMore = $derived(path !== '/' && path !== '/map' && !onAlbums && !path.startsWith('/messages/') && entryOf(path) !== undefined);

	function show() {
		dialog.showModal();
		more = true;
	}
	function hide() {
		dialog.close();
	}
</script>

<nav class="bottom" aria-label="Main">
	<ul>
		<li><a href={listHref} aria-current={path === '/' || path.startsWith('/messages/') ? 'page' : undefined}><Icon name="messages" /><span>Messages</span></a></li>
		<li><a href={mapHref} aria-current={path === '/map' ? 'page' : undefined}><Icon name="map" /><span>Map</span></a></li>
		<li><a href="/albums" aria-current={onAlbums ? 'page' : undefined}><Icon name="albums" /><span>Albums</span></a></li>
		<li>
			<button type="button" aria-haspopup="dialog" aria-expanded={more} aria-current={inMore ? 'page' : undefined} onclick={show}
				><Icon name="more" /><span>More</span></button
			>
		</li>
	</ul>
</nav>

<dialog class="more" bind:this={dialog} aria-labelledby="more-title" onclose={() => (more = false)}>
	<div class="head">
		<h2 id="more-title">More</h2>
		<button class="ghost" type="button" onclick={hide}><Icon name="close" />Close</button>
	</div>
	{#each groups as g (g.id)}
		<h3><Icon name={g.icon} /><a href={g.href} onclick={hide}>{g.label}</a></h3>
		<ul>
			{#each g.entries as e (e.href)}
				<li><a href={e.href} onclick={hide} aria-current={entryOf(path)?.entry === e ? 'page' : undefined}><Icon name={e.icon} />{e.label}</a></li>
			{/each}
		</ul>
	{/each}
	{#if version}<p class="version" data-testid="version">v{version}</p>{/if}
</dialog>

<style>
	.bottom {
		display: none;
	}
	.more {
		width: 100%;
		max-width: 100%;
		max-height: var(--sheet-height);
		margin: auto 0 0;
		padding: var(--space-lg);
		background: var(--surface-2);
		color: var(--fg);
		border: var(--border-width) solid var(--control-border);
		border-radius: var(--radius) var(--radius) 0 0;
	}
	.more::backdrop {
		background: var(--scrim);
	}
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}
	h2 {
		margin: 0;
	}
	h3 {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		margin: var(--space-lg) 0 var(--space-2xs);
		color: var(--muted);
		font-size: var(--text-sm);
	}
	h3 a {
		color: inherit;
	}
	.version {
		margin: var(--space-lg) 0 0;
		padding: 0 var(--space-md);
		color: var(--muted);
		font-size: var(--text-xxs);
		overflow-wrap: anywhere;
	}
	.more ul {
		margin: 0;
		padding: 0;
		list-style: none;
	}
	.more li a {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		min-height: var(--target);
		padding: 0 var(--space-md);
		border-radius: var(--radius-small);
		color: var(--fg);
		text-decoration: none;
	}
	.more li a:hover {
		background: var(--hover);
	}
	.more li a[aria-current] {
		background: var(--accent-soft);
		color: var(--accent-soft-fg);
		font-weight: var(--weight-strong);
	}
	@media (max-width: 47.99rem) {
		.bottom {
			display: block;
			position: fixed;
			left: 0;
			right: 0;
			bottom: 0;
			z-index: var(--layer-header);
			padding-bottom: env(safe-area-inset-bottom);
			background: var(--surface);
			border-top: var(--border-width) solid var(--border);
		}
		.bottom ul {
			display: grid;
			grid-template-columns: repeat(4, 1fr);
			margin: 0;
			padding: 0;
			list-style: none;
		}
		.bottom a,
		.bottom button {
			display: flex;
			flex-direction: column;
			align-items: center;
			justify-content: center;
			gap: var(--space-3xs);
			width: 100%;
			min-height: var(--bottom-bar-height);
			padding: 0;
			border: 0;
			border-radius: 0;
			background: transparent;
			color: var(--muted);
			font-size: var(--text-xs);
			font-weight: var(--weight-normal);
			text-decoration: none;
		}
		.bottom :global(.ui-icon) {
			width: var(--target);
			height: var(--avatar-size);
			padding: var(--space-3xs) var(--space-md);
			border-radius: var(--radius-pill);
		}
		.bottom a:hover :global(.ui-icon),
		.bottom button:hover :global(.ui-icon) {
			background: var(--hover);
		}
		.bottom [aria-current] {
			color: var(--fg);
			font-weight: var(--weight-strong);
		}
		.bottom [aria-current] :global(.ui-icon) {
			background: var(--accent-soft);
			color: var(--accent-soft-fg);
		}
	}
	@media print {
		.bottom {
			display: none;
		}
	}
</style>
