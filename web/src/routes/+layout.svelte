<script lang="ts">
	import '../app.css';
	import { page } from '$app/state';
	import { onMount } from 'svelte';
	import { api, type Meta } from '#lib/api.ts';

	let { children } = $props();
	let meta: Meta | undefined = $state();
	let theme: 'system' | 'light' | 'dark' = $state('system');

	onMount(() => {
		api.meta().then((m) => (meta = m));
		try {
			const saved = localStorage.getItem('theme');
			if (saved === 'light' || saved === 'dark') theme = saved;
		} catch {
			// storage unavailable: follow the system
		}
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

	const nav = $derived([
		{ href: '/', label: 'Messages' },
		{ href: '/capcodes', label: 'Unknown capcodes' },
		...(meta?.role === 'admin' ? [{ href: '/admin/clients', label: 'Clients' }] : [])
	]);
</script>

<a class="skip" href="#main">Skip to content</a>
<header class="top">
	<a class="brand" href="/">PagerArchive</a>
	<nav aria-label="Main">
		{#each nav as item (item.href)}
			<a href={item.href} aria-current={page.url.pathname === item.href ? 'page' : undefined}>{item.label}</a>
		{/each}
	</nav>
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
		z-index: 1;
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
	nav a[aria-current='page'] {
		font-weight: 600;
		border-bottom: 2px solid var(--accent);
	}
	.theme select {
		font-size: 0.85rem;
	}
	main {
		max-width: 64rem;
		margin: 0 auto;
	}
	.bottom {
		max-width: 64rem;
		margin: 1rem auto;
		padding: 0 0.75rem;
		font-size: 0.75rem;
		color: var(--muted);
	}
	.skip {
		position: absolute;
		left: -9999px;
	}
	.skip:focus {
		left: 0.5rem;
		top: 0.5rem;
		z-index: 2;
		background: var(--surface);
		padding: 0.5rem;
	}
</style>
