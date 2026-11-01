<script lang="ts">
	import { afterNavigate, beforeNavigate } from '$app/navigation';
	import { tick, untrack, type Snippet } from 'svelte';
	import { api, type Message, type Meta } from './api.ts';
	import { rangeProblem } from './filters.ts';
	import Icon from './Icon.svelte';
	import { keep, keptFor, type Kept } from './listState.svelte.ts';
	import MessageRow from './MessageRow.svelte';
	import { apiParams, isFiltered } from './search.ts';

	// query: the viewer's search, from the address (search.ts); base: criteria
	// fixed by the view (the alias view's alias, say), in the API's terms. The
	// print page (R67) sets pick, for a tick box on each row, and scroll
	// false: its sheet sits below the results, so older ones load only with
	// the button. The message list sets remember (R81a: coming back restores
	// what it had loaded and where it was scrolled to), selected (the message
	// open beside it, R80), keepScroll (opening one beside it leaves the list
	// where it is) and mapHref (the same search on the map).
	let {
		query,
		base = {},
		level = 1,
		title = 'Messages',
		pick = false,
		scroll = true,
		remember = false,
		selected,
		keepScroll = false,
		mapHref,
		count = $bindable(0),
		more = $bindable(false),
		children
	}: {
		query: URLSearchParams;
		base?: Record<string, string>;
		level?: 1 | 2;
		title?: string;
		pick?: boolean;
		scroll?: boolean;
		remember?: boolean;
		selected?: number;
		keepScroll?: boolean;
		mapHref?: string;
		count?: number;
		more?: boolean;
		/** A line under the heading. */
		children?: Snippet;
	} = $props();

	let meta: Meta | undefined = $state();
	let messages: Message[] = $state([]);
	let next: string | undefined = $state();
	let loading = $state(false);
	let error = $state('');
	let fresh = $state(new Set<number>());
	let announce = $state('');
	let live = $state<'connecting' | 'live' | 'reconnecting'>('connecting');
	let sentinel: HTMLElement | undefined = $state();

	$effect(() => {
		count = messages.length;
		more = !!next;
	});

	// The API's parameters: the search's, then the view's own. An event
	// category is matched against the network's (meta), so this waits for it.
	// Strings, so the list reloads only when they change.
	const asked = $derived.by(() => {
		if (!meta) return undefined;
		const { params, problem } = apiParams(query, meta.categories);
		for (const [k, v] of Object.entries(base)) params.set(k, v);
		return { key: params.toString(), problem };
	});
	const askedKey = $derived(asked?.key);
	const askedProblem = $derived(asked?.problem ?? '');

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
		if (!next || loading || askedKey === undefined) return;
		loading = true;
		try {
			const p = await api.messages(new URLSearchParams(askedKey), next);
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
	function insert(m: Message, quietly = false) {
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
		if (quietly) return;
		fresh = new Set([...fresh, m.id]);
		announce = `New message from ${m.alias ?? m.capcode}`;
	}

	// R81a: the scroll position to go back to, once the navigation that
	// showed the list has finished (SvelteKit scrolls a new page to the top).
	// Its place is the first row in view and where that row was on the
	// screen, so it's found again even if the list is now wider or narrower
	// (a message open beside it, or not) and its rows wrap differently.
	let pendingScroll: Kept['place'] | undefined;
	let settled = false;
	let popped = false;
	let listEl: HTMLElement | undefined = $state();
	function place(): Kept['place'] {
		const below = document.querySelector('header.top')?.getBoundingClientRect().bottom ?? 0;
		const rows = listEl ? [...listEl.querySelectorAll<HTMLElement>('article[data-message-id]')] : [];
		const first = rows.find((r) => r.getBoundingClientRect().bottom > below);
		return { scrollY: window.scrollY, row: first ? Number(first.dataset.messageId) : undefined, top: first?.getBoundingClientRect().top ?? 0 };
	}
	function applyScroll() {
		if (pendingScroll === undefined || !settled) return;
		if (popped) return void (pendingScroll = undefined);
		const p = pendingScroll;
		pendingScroll = undefined;
		requestAnimationFrame(() => {
			window.scrollTo(0, p.scrollY);
			const row = p.row === undefined ? null : listEl?.querySelector(`article[data-message-id="${p.row}"]`);
			if (row) window.scrollBy(0, row.getBoundingClientRect().top - p.top);
		});
	}

	// Load and subscribe whenever the criteria change.
	let restarts = $state(0);
	$effect(() => {
		if (askedKey === undefined) return;
		const q = new URLSearchParams(askedKey);
		void restarts;
		// A reversed range or an unknown category in the address: say so,
		// without asking the API.
		const problem = rangeProblem(q.get('from'), q.get('to')) || askedProblem;
		if (problem) {
			error = problem;
			messages = [];
			next = undefined;
			return;
		}
		const kept = remember ? keptFor(askedKey) : undefined;
		if (kept) {
			// Back to what it had and where it was, then anything newer.
			messages = kept.messages;
			next = kept.next;
			error = '';
			pendingScroll = kept.place;
			tick().then(applyScroll);
			api
				.messages(q)
				.then((p) => p.messages.forEach((m) => insert(m, true)))
				.catch(() => {});
		} else untrack(() => loadFirst(q));
		const es = new EventSource(`/api/v1/stream?${q}`);
		let opened = false;
		let timer: ReturnType<typeof setTimeout> | undefined;
		es.onopen = () => {
			// After a reconnect, reload so nothing sent meanwhile is missed, and
			// what the role allows: a role change ends the stream (ADR 0010 §4).
			// The header hears of it too (the Administration group).
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

	// R81a: keep what it has and where it is, on every way out.
	if (untrack(() => remember)) {
		beforeNavigate(() => {
			if (askedKey !== undefined) keep({ key: askedKey, messages: $state.snapshot(messages) as Message[], next, place: place() });
		});
		afterNavigate((nav) => {
			// The back and forward buttons: the browser and SvelteKit put the
			// page back where it was themselves.
			popped = nav.type === 'popstate';
			settled = true;
			applyScroll();
		});
	}

	// Infinite scroll, with a button as a keyboard-friendly fallback.
	$effect(() => {
		if (!sentinel || !scroll) return;
		const io = new IntersectionObserver((entries) => {
			if (entries.some((e) => e.isIntersecting)) loadMore();
		});
		io.observe(sentinel);
		return () => io.disconnect();
	});
</script>

<div class="bar">
	<svelte:element this={level === 1 ? 'h1' : 'h2'}>{title}</svelte:element>
	<span class="live {live}" role="status"><span class="pulse" aria-hidden="true"></span>{live === 'live' ? 'Live' : live === 'reconnecting' ? 'Reconnecting…' : 'Connecting…'}</span>
	{#if mapHref}
		<div class="views" role="group" aria-label="View">
			<span class="button secondary" aria-current="page"><Icon name="list" />List</span>
			<a class="button secondary" href={mapHref}><Icon name="map" />Map</a>
		</div>
	{/if}
</div>
{@render children?.()}
<div class="visually-hidden" aria-live="polite">{announce}</div>

{#if error}<p class="error" role="alert">{error}</p>{/if}

<div class="list" data-testid="message-list" bind:this={listEl}>
	{#each messages as m (m.id)}
		<MessageRow {m} tz={meta?.time_zone ?? 'UTC'} fresh={fresh.has(m.id)} {pick} selected={selected === m.id} {keepScroll} />
	{/each}
</div>

{#if !loading && asked && messages.length === 0 && !error}
	<p class="status none">No messages{isFiltered(query) || query.has('from') || query.has('to') ? ' match this search' : ' yet'}.</p>
{/if}
{#if next}
	<div bind:this={sentinel} class="more">
		<button class="secondary" type="button" onclick={loadMore} disabled={loading}>{loading ? 'Loading…' : 'Load older messages'}</button>
	</div>
{/if}

<style>
	.bar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-sm) var(--space-lg);
		padding: var(--space-lg) var(--row-pad-x) var(--space-md);
		border-bottom: var(--border-width) solid var(--border);
	}
	.bar h1,
	.bar h2 {
		margin: 0;
	}
	.bar h2 {
		font-size: var(--text-xl);
	}
	.live {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		font-size: var(--text-sm);
		color: var(--muted);
	}
	.pulse {
		width: var(--pulse-size);
		height: var(--pulse-size);
		border-radius: var(--radius-round);
		background: var(--muted);
	}
	.live.live .pulse {
		background: var(--ok-fg);
	}
	.views {
		display: flex;
		margin-left: auto;
	}
	.views > :first-child {
		border-top-right-radius: 0;
		border-bottom-right-radius: 0;
	}
	.views > * + * {
		margin-left: calc(-1 * var(--border-width));
		border-top-left-radius: 0;
		border-bottom-left-radius: 0;
	}
	.none,
	.error {
		padding: var(--space-lg) var(--row-pad-x);
	}
	.more {
		padding: var(--space-lg);
		text-align: center;
	}
</style>
