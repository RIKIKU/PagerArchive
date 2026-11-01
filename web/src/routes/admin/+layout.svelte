<script lang="ts">
	// R44b: every admin screen is in Administration. Non-admins are refused
	// here as well as by the API, so they never see an admin screen's frame.
	// The breadcrumbs are the layout's (R79).
	import { api, type Meta } from '#lib/api.ts';

	let { children } = $props();
	let meta: Meta | undefined = $state();
	let failed = $state(false);
	api
		.meta()
		.then((m) => (meta = m))
		.catch(() => (failed = true));
</script>

{#if meta?.role === 'admin'}
	{@render children()}
{:else if meta || failed}
	<h1>Administration</h1>
	<p class="error" role="alert">This page is for admins only.</p>
{/if}
