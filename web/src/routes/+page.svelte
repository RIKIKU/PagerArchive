<script lang="ts">
	import { page } from '$app/state';
	import { api, type Message, type Meta } from '#lib/api.ts';
	import { criteria } from '#lib/filters.ts';
	import Filters from '#lib/Filters.svelte';
	import MessageRow from '#lib/MessageRow.svelte';

	let meta: Meta | undefined = $state();
	let messages: Message[] = $state([]);
	let next: string | undefined = $state();
	let loading = $state(false);
	let error = $state('');
	let fresh = $state(new Set<number>());
	let announce = $state('');
	let live = $state<'connecting' | 'live' | 'reconnecting'>('connecting');
	let sentinel: HTMLElement | undefined = $state();

	const query = $derived(criteria(page.url));
	const queryKey = $derived(query.toString());

	api.meta().then((m) => (meta = m)).catch((e) => (error = e.message));

	async function loadFirst(q: URLSearchParams) {
		loading = true;
		error = '';
		try {
			const p = await api.messages(q);
			messages = p.messages;
			next = p.next;
		} catch (e) {
			error = (e as Error).message;
		} finally {
			loading = false;
		}
	}

	async function loadMore() {
		if (!next || loading) return;
		loading = true;
		try {
			const p = await api.messages(query, next);
			const seen = new Set(messages.map((m) => m.id));
			messages = [...messages, ...p.messages.filter((m) => !seen.has(m.id))];
			next = p.next;
		} catch (e) {
			error = (e as Error).message;
		} finally {
			loading = false;
		}
	}

	// Newest first by sent time; a late buffered page goes in its sorted
	// position (section 14).
	function insert(m: Message) {
		if (messages.some((x) => x.id === m.id)) return;
		const key = (x: Message) => [Date.parse(x.sent_at), x.id] as const;
		const [t, id] = key(m);
		let i = messages.findIndex((x) => {
			const [xt, xid] = key(x);
			return xt < t || (xt === t && xid < id);
		});
		if (i === -1) {
			if (next) return; // older than everything loaded: it'll come with paging
			i = messages.length;
		}
		messages = [...messages.slice(0, i), m, ...messages.slice(i)];
		fresh = new Set([...fresh, m.id]);
		announce = `New message from ${m.alias ?? m.capcode}`;
	}

	// Load and subscribe whenever the criteria change.
	$effect(() => {
		const q = new URLSearchParams(queryKey);
		loadFirst(q);
		const es = new EventSource(`/api/v1/stream?${q}`);
		let opened = false;
		es.onopen = () => {
			// After a reconnect, reload so nothing sent meanwhile is missed.
			if (opened) loadFirst(q);
			opened = true;
			live = 'live';
		};
		es.onerror = () => (live = 'reconnecting');
		es.addEventListener('message', (ev) => insert(JSON.parse((ev as MessageEvent).data)));
		return () => es.close();
	});

	// Infinite scroll, with a button as a keyboard-friendly fallback.
	$effect(() => {
		if (!sentinel) return;
		const io = new IntersectionObserver((entries) => {
			if (entries.some((e) => e.isIntersecting)) loadMore();
		});
		io.observe(sentinel);
		return () => io.disconnect();
	});
</script>

<svelte:head><title>Messages · PagerArchive</title></svelte:head>

{#if meta}
	<Filters {query} {meta} />
{/if}
<div class="bar">
	<h1>Messages</h1>
	<span class="live {live}" role="status">{live === 'live' ? 'Live' : live === 'reconnecting' ? 'Reconnecting…' : 'Connecting…'}</span>
</div>
<div class="visually-hidden" aria-live="polite">{announce}</div>

{#if error}<p class="error" role="alert">{error}</p>{/if}

<div class="list" data-testid="message-list">
	{#each messages as m (m.id)}
		<MessageRow {m} tz={meta?.time_zone ?? 'UTC'} fresh={fresh.has(m.id)} />
	{/each}
</div>

{#if !loading && messages.length === 0 && !error}
	<p class="status">No messages{query.toString() ? ' match this search' : ' yet'}.</p>
{/if}
{#if next}
	<div bind:this={sentinel} class="more">
		<button type="button" onclick={loadMore} disabled={loading}>{loading ? 'Loading…' : 'Load older messages'}</button>
	</div>
{/if}

<style>
	.bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding-right: 0.75rem;
	}
	.live {
		font-size: 0.8rem;
		color: var(--muted);
	}
	.live.live::before {
		content: '●';
		color: #2e7d32;
		margin-right: 0.3rem;
	}
	.more {
		padding: 1rem;
		text-align: center;
	}
</style>
