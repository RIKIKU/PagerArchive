<script lang="ts">
	// R44b: every admin screen is in Administration. Non-admins are refused
	// here as well as by the API, so they never see an admin screen's frame.
	import { page } from '$app/state';
	import { api, type Meta } from '#lib/api.ts';

	let { children } = $props();
	let meta: Meta | undefined = $state();
	let failed = $state(false);
	api
		.meta()
		.then((m) => (meta = m))
		.catch(() => (failed = true));
	const start = $derived(page.url.pathname.replace(/\/$/, '') === '/admin');
</script>

{#if meta?.role === 'admin'}
	{#if !start}
		<nav class="crumbs" aria-label="Breadcrumb"><a href="/admin">Administration</a></nav>
	{/if}
	{@render children()}
{:else if meta || failed}
	<h1>Administration</h1>
	<p class="error" role="alert">This page is for admins only.</p>
{/if}

<style>
	.crumbs {
		margin: 0.75rem 0.75rem 0;
		font-size: var(--text-sm);
	}
</style>
