<script lang="ts">
	import { page } from '$app/state';
	import { api, type Message, type Meta } from '#lib/api.ts';
	import MessageRow from '#lib/MessageRow.svelte';

	let meta: Meta | undefined = $state();
	let messages: Message[] = $state([]);
	let error = $state('');
	let loaded = $state(false);
	const job = $derived(page.params.job ?? '');

	api.meta().then((m) => (meta = m));
	$effect(() => {
		loaded = false;
		api
			.job(job)
			.then((j) => (messages = j.messages))
			.catch((e) => (error = e.message))
			.finally(() => (loaded = true));
	});
</script>

<svelte:head><title>Job {job} · PagerArchive</title></svelte:head>

<h1>Job {job}</h1>
<p class="status">
	{#if loaded}{messages.length} message{messages.length === 1 ? '' : 's'}, newest first.{/if}
	Pages are grouped by job number on a best-effort basis; the app doesn't track an incident's state.
</p>
{#if error}<p class="error" role="alert">{error}</p>{/if}
<div class="list">
	{#each messages as m (m.id)}
		<MessageRow {m} tz={meta?.time_zone ?? 'UTC'} />
	{/each}
</div>
