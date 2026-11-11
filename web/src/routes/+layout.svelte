<script lang="ts">
	// The frame of every signed-in page (R77-R81, ADR 0015): the side menu,
	// the top bar with the breadcrumbs, the search box and the account menu,
	// and on phones a bottom bar instead of the side menu. The page itself
	// uses the full width (R80).
	import '../app.css';
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import { api, type Me, type Meta } from '#lib/api.ts';
	import AccountMenu from '#lib/AccountMenu.svelte';
	import BottomBar from '#lib/BottomBar.svelte';
	import Crumbs from '#lib/Crumbs.svelte';
	import { here } from '#lib/here.svelte.ts';
	import { list, listHref } from '#lib/listState.svelte.ts';
	import Menu from '#lib/Menu.svelte';
	import { menu } from '#lib/menu.svelte.ts';
	import { crumbs } from '#lib/nav.ts';
	import SearchBox from '#lib/SearchBox.svelte';
	import { criteria } from '#lib/search.ts';
	import { loadSheet, sheet } from '#lib/sheet.svelte.ts';
	import { refreshWaiting, waiting } from '#lib/waiting.svelte.ts';

	let { children } = $props();
	let meta: Meta | undefined = $state();
	let me: Me | undefined = $state();
	let theme: 'system' | 'light' | 'dark' = $state('system');
	let topHeight = $state(0);

	// The sign-in page works without a session, and has no frame.
	const signedOut = $derived(page.url.pathname.startsWith('/signin'));

	onMount(() => {
		if (!signedOut)
			api
				.meta()
				.then((m) => (meta = m))
				.catch(() => {}); // 401 goes to the sign-in page
		// A live list reloads meta when its stream reconnects, e.g. after a role
		// change (ADR 0010 §4).
		const update = (e: Event) => (meta = (e as CustomEvent<Meta>).detail);
		window.addEventListener('pagerarchive:meta', update);
		// R67: the print sheet is kept per user (their ID is in its key).
		let unfollow = () => {};
		if (!signedOut)
			api
				.me()
				.then((m) => {
					me = m;
					unfollow = loadSheet(m.id);
				})
				.catch(() => {});
		try {
			const saved = localStorage.getItem('theme');
			if (saved === 'light' || saved === 'dark') theme = saved;
		} catch {
			// storage unavailable: follow the system
		}
		return () => {
			window.removeEventListener('pagerarchive:meta', update);
			unfollow();
		};
	});

	$effect(() => {
		if (theme === 'system') delete document.documentElement.dataset.theme;
		else document.documentElement.dataset.theme = theme;
		try {
			localStorage.setItem('theme', theme);
		} catch {
			// ignore
		}
	});

	// R39: admins see at a glance when a receiver has gone quiet; R55: and how
	// many alias requests wait for review (the count on Administration).
	let quiet = $state(0);
	$effect(() => {
		if (meta?.role !== 'admin') return;
		const check = () =>
			api
				.clients()
				.then((cs) => (quiet = cs.filter((c) => !c.receiving).length))
				.catch(() => {});
		const checkAll = () => (check(), refreshWaiting());
		checkAll();
		const t = setInterval(checkAll, 60_000);
		return () => clearInterval(t);
	});

	const admin = $derived(meta?.role === 'admin');
	const route = $derived(page.route.id ?? '');
	const path = $derived(page.url.pathname);
	const title = $derived(here.path === path ? here.title : '');
	const trail = $derived(crumbs(route, page.params as Record<string, string>, title));

	// R33a: the search box applies to the view it's on, if that view lists
	// messages; elsewhere it searches the message list. On a message's own
	// page, it shows the list's search.
	const searchable = ['/(list)', '/map', '/aliases/[id]', '/capcodes/[capcode]', '/jobs/[job]', '/sheet'];
	const own = $derived(searchable.includes(route));
	const action = $derived(own ? path : '/');
	const query = $derived(own ? criteria(page.url) : route === '/(list)/messages/[id]' ? new URLSearchParams(list.query) : new URLSearchParams());
	const mapHref = $derived(list.query ? `/map?${list.query}` : '/map');
</script>

{#if signedOut}
	<main id="main" class="bare">{@render children()}</main>
{:else}
	<a class="skip" href="#main">Skip to content</a>
	<div class="shell" class:shrunk={menu.shrunk} style:--topbar-height="{topHeight}px">
		<nav class="side" aria-label="Main">
			<Menu {path} {admin} listHref={listHref()} waiting={waiting.count} sheetCount={sheet.ids.length} {quiet} />
		</nav>
		<header class="top" bind:clientHeight={topHeight}>
			<Crumbs {trail} listHref={listHref()} />
			<div class="tools">
				{#if meta}<SearchBox {action} {query} {admin} categories={meta.categories} timeZone={meta.time_zone} firstPage={meta.first_page} lastPage={meta.last_page} />{/if}
				<AccountMenu {me} bind:theme />
			</div>
		</header>
		<main id="main">
			{@render children()}
		</main>
		{#if meta?.attribution}
			<footer class="note">Address data: {meta.attribution}</footer>
		{/if}
		<BottomBar {path} {admin} listHref={listHref()} {mapHref} />
	</div>
{/if}

<style>
	.shell {
		display: grid;
		grid-template-columns: var(--menu-width) minmax(0, 1fr);
		grid-template-rows: auto 1fr auto;
		grid-template-areas: 'side top' 'side main' 'side note';
		min-height: var(--full-height);
	}
	.shell.shrunk {
		grid-template-columns: var(--menu-width-shrunk) minmax(0, 1fr);
	}
	.side {
		grid-area: side;
		position: sticky;
		top: 0;
		z-index: var(--layer-header);
		height: var(--full-height);
		overflow-y: auto;
		background: var(--surface);
		border-right: var(--border-width) solid var(--border);
	}
	.shrunk .side {
		overflow: visible;
	}
	.top {
		grid-area: top;
		position: sticky;
		top: 0;
		z-index: var(--layer-header);
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-sm) var(--space-lg);
		padding: var(--space-sm) var(--page-pad);
		background: var(--bg);
		border-bottom: var(--border-width) solid var(--border);
	}
	.tools {
		display: flex;
		flex: 1 1 auto;
		align-items: center;
		justify-content: flex-end;
		gap: var(--space-sm);
		min-width: 0;
	}
	.tools > :global(.search) {
		flex: 1 1 auto;
		max-width: var(--measure-wide);
	}
	main {
		grid-area: main;
		min-width: 0;
		padding: var(--page-pad);
		/* Its own stacking context: content stays below the top bar. */
		isolation: isolate;
	}
	main.bare {
		max-width: var(--measure);
		margin: 0 auto;
	}
	.note {
		grid-area: note;
		padding: 0 var(--page-pad) var(--space-lg);
		font-size: var(--text-xxs);
		color: var(--muted);
	}
	/* Off the screen, above it, until it has focus. */
	.skip {
		position: absolute;
		left: 0;
		top: 0;
		transform: translateY(-200%);
	}
	.skip:focus {
		transform: translate(var(--space-sm), var(--space-sm));
		z-index: var(--layer-overlay);
		background: var(--surface-2);
		padding: var(--space-sm) var(--space-lg);
	}
	/* Phones (R81): no side menu; the bottom bar instead. */
	@media (max-width: 47.99rem) {
		.shell,
		.shell.shrunk {
			display: block;
			padding-bottom: calc(var(--bottom-bar-height) + env(safe-area-inset-bottom));
		}
		.side {
			display: none;
		}
		.top {
			padding: var(--space-sm) var(--page-pad-phone);
		}
		.tools {
			flex-basis: 100%;
			flex-wrap: nowrap;
			align-items: flex-start;
		}
		main {
			padding: var(--page-pad-phone);
		}
		.note {
			padding: 0 var(--page-pad-phone) var(--space-lg);
		}
	}
	/* A printout has no frame (R68), nor SvelteKit's hidden announcement of
	   the page's title. */
	@media print {
		.skip,
		.side,
		.top,
		.note,
		:global(#svelte-announcer) {
			display: none;
		}
		.shell {
			display: block;
		}
		main {
			padding: 0;
		}
	}
</style>
