<script lang="ts">
	// The frame of every signed-in page (R77-R81, ADR 0015): the side menu,
	// the top bar with the breadcrumbs, the search box and the account menu,
	// and on phones a bottom bar instead of the side menu. The page itself
	// uses the full width (R80). R77d (ADR 0051): the frame is ArchiveUx's
	// (Frame, Menu, BottomBar, AccountMenu), filled with PagerArchive's menu
	// tree, counts and dots (frame.ts); R77e: with RadioArchive set, its app
	// switcher, for whom R77f says.
	// ArchiveUx's tokens and base styles (ADR 0049), then PagerArchive's own
	// (the map's), in that order.
	import 'archiveux/archiveux.css';
	import '../app.css';
	import { beforeNavigate } from '$app/navigation';
	import { page, updated } from '$app/state';
	import { onMount } from 'svelte';
	import { AccountMenu, BottomBar, Frame, Menu } from 'archiveux/components';
	import { api, type Look, type Me, type Meta } from '#lib/api.ts';
	import { albums, refreshAlbums } from '#lib/albums.svelte.ts';
	import Crumbs from '#lib/Crumbs.svelte';
	import { bottomCurrent, bottomEntries, menuCurrent, menuGroups, menuTop, moreGroups, NEW_GROUP, type Marks } from '#lib/frame.ts';
	import { here } from '#lib/here.svelte.ts';
	import { icons } from '#lib/icons.ts';
	import { followLargeEventDot, largeEventDot } from '#lib/largeEventDot.svelte.ts';
	import { list, listHref } from '#lib/listState.svelte.ts';
	import ReceiverAlert from '#lib/ReceiverAlert.svelte';
	import { releaseHref } from '#lib/release.ts';
	import ReleaseNotice from '#lib/ReleaseNotice.svelte';
	import { menu as menuState, toggleGroup, toggleShrunk } from '#lib/menu.svelte.ts';
	import { crumbs } from '#lib/nav.ts';
	import SearchBox from '#lib/SearchBox.svelte';
	import { criteria } from '#lib/search.ts';
	import { scrubberHere } from '#lib/scrubberHere.svelte.ts';
	import { loadSheet, sheet } from '#lib/sheet.svelte.ts';
	import { followTopBar } from '#lib/statusBar.ts';
	import { refreshWaiting, waiting } from '#lib/waiting.svelte.ts';

	let { children } = $props();
	let meta: Meta | undefined = $state();
	let me: Me | undefined = $state();
	let theme: 'system' | 'light' | 'dark' = $state('system');
	// R82d: the user's look, from their account. The server sent the page
	// with it already on the root (data-look), so the first paint is theirs.
	// Until their account loads, the one the server put there.
	const sentLook = document.documentElement.dataset.look;
	let look: Look = $state(sentLook === 'console' ? 'console' : 'harbour');

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
					look = m.look;
					unfollow = loadSheet(m.id);
					// R77: the Albums group lists their albums.
					refreshAlbums();
				})
				.catch(() => {});
		try {
			const saved = localStorage.getItem('theme');
			if (saved === 'light' || saved === 'dark') theme = saved;
		} catch {
			// storage unavailable: follow the system
		}
		// R90: an installed app's title and status bars follow the look and theme.
		const unfollowTopBar = followTopBar();
		// R77c: the new-large-event dot, live.
		const unfollowDot = signedOut ? () => {} : followLargeEventDot();
		return () => {
			window.removeEventListener('pagerarchive:meta', update);
			unfollow();
			unfollowTopBar();
			unfollowDot();
		};
	});

	// R90 (ADR 0037): a release that came out while the app was open, found
	// when it's brought back to the front (or within the hour), is loaded
	// whole on the next tap, with the new release's service worker, rather
	// than going on with the old one's code. An installed app is often kept
	// open for days.
	beforeNavigate(({ willUnload, to }) => {
		if (updated.current && !willUnload && to?.url) location.href = to.url.href;
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

	// A look chosen in the account menu applies at once, without reloading
	// (R82d). Pages before sign-in are Harbour.
	$effect(() => {
		if (signedOut) delete document.documentElement.dataset.look;
		else if (look && document.documentElement.dataset.look !== look) document.documentElement.dataset.look = look;
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
	const searchable = ['/(list)', '/map', '/aliases/[id]', '/capcodes/[capcode]', '/jobs/[job]', '/albums/[id]', '/sheet'];
	const own = $derived(searchable.includes(route));
	const action = $derived(own ? path : '/');
	const query = $derived(own ? criteria(page.url) : route === '/(list)/messages/[id]' ? new URLSearchParams(list.query) : new URLSearchParams());
	const mapHref = $derived(list.query ? `/map?${list.query}` : '/map');

	// R77d: the menu's tree and marks, for ArchiveUx's Menu and BottomBar.
	const marks: Marks = $derived({ admin, waiting: waiting.count, sheetCount: sheet.ids.length, newEvent: largeEventDot.on, albums: albums.list });
	const home = $derived(listHref());
	const version = $derived(meta?.version ?? '');
	// An album's page keeps the Albums group open, listed yet or not.
	const opened = $derived(path.startsWith('/albums/') ? { ...menuState.open, albums: true } : menuState.open);
	// R77e, R77f: the switcher, when RadioArchive is set and the server says
	// this user sees it.
	const otherApp = $derived(meta?.radioarchive_url && me?.show_switcher ? { name: meta.radioarchive_name ?? 'Radio', href: meta.radioarchive_url } : null);
</script>

{#if signedOut}
	<main id="main" class="bare">{@render children()}</main>
{:else}
	<Frame shrunk={menuState.shrunk} {otherApp}>
		{#snippet notices()}
			{#if scrubberHere.focus}
				<!-- R81b: the scrubber, without tabbing through the menu and every row (also Alt+J). -->
				<a
					class="skip"
					href="#main"
					aria-keyshortcuts="Alt+J"
					onclick={(e) => {
						e.preventDefault();
						scrubberHere.focus?.();
					}}>Skip to the date scrubber (Alt+J)</a
				>
			{/if}
			<!-- R77b: told of a new release once; early in the tab order, as it's on screen only briefly. -->
			<ReleaseNotice />
		{/snippet}
		{#snippet menu()}
			<Menu
				brand={{ name: 'PagerArchive', mark: 'PA', href: home }}
				current={menuCurrent(path, home)}
				top={menuTop(home)}
				groups={menuGroups(marks)}
				open={opened}
				ontoggle={toggleGroup}
				shrunk={menuState.shrunk}
				onshrink={toggleShrunk}
				{version}
				versionHref={version ? releaseHref(version) : undefined}
			>
				{#snippet foot({ shrunk }: { shrunk: boolean })}
					{#if admin && quiet > 0}<ReceiverAlert {quiet} {shrunk} />{/if}
				{/snippet}
			</Menu>
		{/snippet}
		{#snippet start()}
			<Crumbs {trail} listHref={home} />
		{/snippet}
		{#snippet tools()}
			{#if meta}<SearchBox {action} {query} {admin} categories={meta.categories} timeZone={meta.time_zone} firstPage={meta.first_page} lastPage={meta.last_page} />{/if}
		{/snippet}
		{#snippet account()}
			<AccountMenu
				account={me ? { name: me.name ?? undefined, email: me.email } : {}}
				bind:theme
				bind:look
				onlook={(l: Look) => api.setLook(l)}
				onsignout={() => api.signOut()}
				links={[{ href: '/account/profile', label: 'Profile', svg: icons.profile }]}
			/>
		{/snippet}
		{#snippet bottomBar()}
			<!-- Below the page, beside the menu (the frame's last row, as no page here has a dock). -->
			{#if meta?.attribution}
				<footer class="note">Address data: {meta.attribution}</footer>
			{/if}
			<BottomBar
				entries={bottomEntries(home, mapHref)}
				groups={moreGroups(marks)}
				current={bottomCurrent(path, home, mapHref)}
				moreDot={largeEventDot.on}
				moreSays={largeEventDot.on ? NEW_GROUP : ''}
				{version}
				versionHref={version ? releaseHref(version) : undefined}
			/>
		{/snippet}
		{@render children()}
	</Frame>
{/if}

<style>
	/* The frame is ArchiveUx's (R77d); these are PagerArchive's parts in it. */
	:global(header.top .tools > .search) {
		flex: 1 1 auto;
		max-width: var(--measure-wide);
	}
	main.bare {
		max-width: var(--measure);
		margin: 0 auto;
	}
	/* Below the page, beside the side menu: the frame's last row. */
	.note {
		grid-area: dock;
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
	/* Phones (R81): the search box and the account menu on a row of their own,
	   below the breadcrumbs. */
	@media (max-width: 47.99rem) {
		:global(header.top > .tools) {
			flex-basis: 100%;
			flex-wrap: nowrap;
			align-items: flex-start;
		}
		.note {
			padding: 0 var(--page-pad-phone) var(--space-lg);
		}
	}
	/* A printout has no frame (R68), nor SvelteKit's hidden announcement of
	   the page's title. */
	@media print {
		.skip,
		.note,
		:global(#svelte-announcer) {
			display: none;
		}
	}
</style>
