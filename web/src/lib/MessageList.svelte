<script lang="ts">
	import { api, type Message, type Meta } from './api.ts';
	import Filters from './Filters.svelte';
	import { rangeProblem } from './filters.ts';
	import MessageRow from './MessageRow.svelte';

	// base: criteria fixed by the view (the alias view's alias, say), added to
	// the viewer's own search criteria from the URL.
	let {
		query,
		base = {},
		level = 1
	}: { query: URLSearchParams; base?: Record<string, string>; level?: 1 | 2 } = $props();

	let meta: Meta | undefined = $state();
	let messages: Message[] = $state([]);
	let next: string | undefined = $state();
	let loading = $state(false);
	let error = $state('');
	let fresh = $state(new Set<number>());
	let announce = $state('');
	let live = $state<'connecting' | 'live' | 'reconnecting'>('connecting');
	let sentinel: HTMLElement | undefined = $state();

	const full = $derived.by(() => {
		const q = new URLSearchParams(query);
		for (const [k, v] of Object.entries(base)) q.set(k, v);
		return q;
	});
	const queryKey = $derived(full.toString());

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
			const p = await api.messages(full, next);
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
	let restarts = $state(0);
	$effect(() => {
		const q = new URLSearchParams(queryKey);
		void restarts;
		// A reversed range in the URL: say so, without asking the API.
		const problem = rangeProblem(q.get('from'), q.get('to'));
		if (problem) {
			error = problem;
			messages = [];
			next = undefined;
			return;
		}
		loadFirst(q);
		const es = new EventSource(`/api/v1/stream?${q}`);
		let opened = false;
		let timer: ReturnType<typeof setTimeout> | undefined;
		es.onopen = () => {
			// After a reconnect, reload so nothing sent meanwhile is missed, and
			// what the role allows: a role change ends the stream (ADR 0010 §4).
			// The header hears of it too (the Admin link).
			if (opened) {
				loadFirst(q);
				api
					.refreshMeta()
					.then((m) => {
						meta = m;
						window.dispatchEvent(new CustomEvent<Meta>('pagerarchive:meta', { detail: m }));
					})
					.catch(() => {});
			}
			opened = true;
			live = 'live';
		};
		es.onerror = () => {
			live = 'reconnecting';
			// The browser gives up on a refused stream (e.g. signed out or
			// deactivated): check the session, which goes to the sign-in
			// page if it has ended, and otherwise try again shortly.
			if (es.readyState === EventSource.CLOSED) {
				timer = setTimeout(() => api.me().then(() => restarts++, () => {}), 3000);
			}
		};
		es.addEventListener('message', (ev) => insert(JSON.parse((ev as MessageEvent).data)));
		return () => {
			clearTimeout(timer);
			es.close();
		};
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

{#if meta}
	<Filters {query} {meta} />
{/if}
<div class="bar">
	<svelte:element this={level === 1 ? 'h1' : 'h2'}>Messages</svelte:element>
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
	.bar h2 {
		font-size: var(--text-xl);
	}
	.live {
		font-size: var(--text-xs);
		color: var(--muted);
	}
	.live.live::before {
		content: '●';
		color: var(--ok-fg);
		margin-right: 0.3rem;
	}
	.more {
		padding: 1rem;
		text-align: center;
	}
</style>
