<script lang="ts">
	import '../app.css';
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import { api, type Meta } from '#lib/api.ts';
	import { refreshWaiting, waiting } from '#lib/waiting.svelte.ts';
	import { loadSheet, sheet } from '#lib/sheet.svelte.ts';

	let { children } = $props();
	let meta: Meta | undefined = $state();
	let theme: 'system' | 'light' | 'dark' = $state('system');

	onMount(() => {
		// The sign-in page works without a session.
		if (!page.url.pathname.startsWith('/signin')) api
				.meta()
				.then((m) => (meta = m))
				.catch(() => {}); // 401 goes to the sign-in page
		// A live list reloads meta when its stream reconnects, e.g. after a role
		// change (ADR 0010 §4).
		const update = (e: Event) => (meta = (e as CustomEvent<Meta>).detail);
		window.addEventListener('pagerarchive:meta', update);
		// R67: the print sheet is kept per user (their ID is in its key).
		let unfollow = () => {};
		if (!page.url.pathname.startsWith('/signin')) api
				.me()
				.then((me) => (unfollow = loadSheet(me.id)))
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
	// many alias requests wait for review (the count on the Admin link).
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

	const current = (href: string) =>
		href === '/admin' ? page.url.pathname === '/admin' || page.url.pathname.startsWith('/admin/') : page.url.pathname === href;

	const nav = $derived(page.url.pathname.startsWith('/signin') ? [] : [
		{ href: '/', label: 'Messages' },
		{ href: '/map', label: 'Map' },
		{ href: '/reports', label: 'Reports' },
		{ href: '/capcodes', label: 'Unknown capcodes' },
		...(meta ? [{ href: '/integrations', label: 'Integrations' }] : []),
		...(meta ? [{ href: '/requests', label: 'Requests' }] : []),
		...(meta ? [{ href: '/sheet', label: 'Print sheet' }] : []),
		// R44b: one Admin link, for admins only.
		...(meta?.role === 'admin' ? [{ href: '/admin', label: 'Admin' }] : []),
		...(meta ? [{ href: '/account', label: 'Account' }] : [])
	]);
</script>

<a class="skip" href="#main">Skip to content</a>
<header class="top">
	<a class="brand" href="/">PagerArchive</a>
	<nav aria-label="Main">
		{#each nav as item (item.href)}
			{#if item.href === '/admin' && waiting.count > 0}
				<a
					href={item.href}
					aria-current={current(item.href) ? 'page' : undefined}
					data-testid="admin-link"
					aria-label="{item.label}, {waiting.count} alias request{waiting.count === 1 ? '' : 's'} waiting"
					>{item.label}<span class="count">{waiting.count}</span></a
				>
			{:else if item.href === '/sheet'}
				<!-- R67: the sheet's link shows how many messages it holds. -->
				<a
					href={item.href}
					aria-current={current(item.href) ? 'page' : undefined}
					data-testid="sheet-link"
					aria-label="{item.label}, {sheet.ids.length} message{sheet.ids.length === 1 ? '' : 's'}"
					>{item.label}<span class="count">{sheet.ids.length}</span></a
				>
			{:else}
				<a href={item.href} aria-current={current(item.href) ? 'page' : undefined} data-testid={item.href === '/admin' ? 'admin-link' : undefined}>{item.label}</a>
			{/if}
		{/each}
	</nav>
	{#if quiet > 0}
		<a class="quiet" href="/admin/clients" data-testid="not-receiving"
			>{quiet} receiver{quiet === 1 ? '' : 's'} not receiving</a
		>
	{/if}
	<label class="theme">
		<span class="visually-hidden">Colour theme</span>
		<select bind:value={theme}>
			<option value="system">Auto</option>
			<option value="light">Light</option>
			<option value="dark">Dark</option>
		</select>
	</label>
</header>
<main id="main">
	{@render children()}
</main>
{#if meta?.attribution}
	<footer class="bottom">Address data: {meta.attribution}</footer>
{/if}

<style>
	.top {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem 1rem;
		padding: 0.5rem 0.75rem;
		border-bottom: 1px solid var(--border);
		background: var(--surface);
		position: sticky;
		top: 0;
		z-index: var(--layer-header);
	}
	.quiet {
		background: var(--warn-bg);
		color: var(--warn-fg);
		padding: 0.15rem 0.5rem;
		border-radius: 0.25rem;
		font-size: var(--text-sm);
		font-weight: 600;
	}
	.brand {
		font-weight: 700;
		color: var(--fg);
		text-decoration: none;
	}
	nav {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem 0.9rem;
		flex: 1;
	}
	nav a {
		color: var(--fg);
		text-decoration: none;
		padding: 0.2rem 0;
	}
	.count {
		display: inline-block;
		margin-left: 0.3rem;
		min-width: 1.25rem;
		padding: 0 0.35rem;
		border-radius: 1rem;
		background: var(--accent);
		color: var(--bg);
		font-size: var(--text-xs);
		font-weight: 600;
		text-align: center;
	}
	nav a[aria-current='page'] {
		font-weight: 600;
		border-bottom: 2px solid var(--accent);
	}
	.theme {
		margin-left: auto;
	}
	.theme select {
		font-size: var(--text-sm);
	}
	/* Phones: brand and theme on one row, the links in a row below, so the
	   header stays short and the first message stays on screen (R31). */
	@media (max-width: 40rem) {
		.top {
			row-gap: 0.15rem;
			padding-bottom: 0.25rem;
		}
		/* One row of links that scrolls sideways within itself (the page
		   never does), however many links there are. */
		nav {
			order: 3;
			flex-basis: 100%;
			flex-wrap: nowrap;
			overflow-x: auto;
			white-space: nowrap;
			scrollbar-width: none;
		}
		.quiet {
			order: 4;
		}
	}
	main {
		max-width: 64rem;
		margin: 0 auto;
		/* Its own stacking context: content stays below the header (app.css). */
		isolation: isolate;
	}
	.bottom {
		max-width: 64rem;
		margin: 1rem auto;
		padding: 0 0.75rem;
		font-size: var(--text-xxs);
		color: var(--muted);
	}
	.skip {
		position: absolute;
		left: -9999px;
	}
	/* A printout has no header, navigation or footer (R68), nor SvelteKit's
	   hidden announcement of the page's title. */
	@media print {
		.skip,
		.top,
		.bottom,
		:global(#svelte-announcer) {
			display: none;
		}
		main {
			max-width: none;
		}
	}
	.skip:focus {
		left: 0.5rem;
		top: 0.5rem;
		z-index: var(--layer-overlay);
		background: var(--surface);
		padding: 0.5rem;
	}
</style>
