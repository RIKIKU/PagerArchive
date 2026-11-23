<script lang="ts">
	// R87c: one large event: its first and last pages' times, its numbers and
	// its job numbers (no levels or codes; owner, 2026-11-13), and then its
	// pages like the message list (P1, P2: the full text, as stored), with the
	// scrubber over its days (R81b) and live updates as pages join.
	import { page } from '$app/state';
	import { api, type AreaRef, type LargeEvent, type Meta } from '#lib/api.ts';
	import AreaTags from '#lib/AreaTags.svelte';
	import { dateTime, isoAttr } from '#lib/format.ts';
	import { setTitle } from '#lib/here.svelte.ts';
	import { numbers, span } from '#lib/largeEvents.ts';
	import MessageList from '#lib/MessageList.svelte';
	import { criteria } from '#lib/search.ts';

	const id = $derived(page.params.id ?? '');
	const query = $derived(criteria(page.url));
	let meta: Meta | undefined = $state();
	let ev: LargeEvent | undefined = $state();
	let error = $state('');
	const tz = $derived(meta?.time_zone ?? 'UTC');

	api.meta().then((m) => (meta = m));
	$effect(() => {
		const want = id;
		api
			.largeEvent(want)
			.then((e) => {
				ev = e;
				areas = e.areas;
				error = '';
			})
			.catch((e) => {
				ev = undefined;
				error = e.status === 404 ? 'There’s no such large event. Retention may have deleted its pages.' : e.message;
			});
	});
	// R87c, R88b: the areas its pages are in, above them, asked again as
	// pages join (a new page may be in another), as a job's view does.
	let count = $state(0);
	let areas: AreaRef[] = $state([]);
	$effect(() => {
		void count;
		const asked = id;
		if (!meta?.areas?.length) return;
		api
			.largeEventAreas(asked)
			.then((a) => {
				if (asked === id) areas = a.areas;
			})
			.catch(() => {});
	});
	const title = $derived(ev && meta ? `Large event, ${dateTime(ev.start, tz)}` : '');
	$effect(() => {
		if (title) setTitle(title);
	});
</script>

<svelte:head><title>{title || 'Large event'} · PagerArchive</title></svelte:head>

{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if ev && meta}
	<div class="event">
		<MessageList {query} base={{ event: String(ev.id), ignored: 'true' }} title={title} bind:count>
			<div class="about" data-testid="event-about">
				<AreaTags {meta} {areas} label="Areas, from where its pages were placed" testid="event-areas" />
				<p class="numbers">{numbers(ev)}</p>
				<dl>
					<dt>First page</dt>
					<dd class="when"><time datetime={isoAttr(ev.start)}>{dateTime(ev.start, tz)}</time></dd>
					<dt>Last page</dt>
					<dd class="when"><time datetime={isoAttr(ev.end)}>{span(ev, tz)}</time></dd>
					{#if ev.job_list?.length}
						<dt>Job numbers</dt>
						<dd class="jobs">
							{#each ev.job_list as j (j)}<a href="/jobs/{encodeURIComponent(j)}">{j}</a>{/each}
						</dd>
					{/if}
				</dl>
				<p class="hint">
					Pages within 12 hours of each other are grouped, so several incidents may share an event. The app doesn't track an incident's
					state.
				</p>
			</div>
		</MessageList>
	</div>
{/if}

<style>
	/* The list runs edge to edge, as the message list does. */
	.event {
		margin: calc(-1 * var(--page-pad));
	}
	.about {
		padding: var(--space-sm) var(--row-pad-x);
		border-bottom: var(--border-width) solid var(--border);
	}
	.about p {
		margin: 0 0 var(--space-xs);
	}
	.about > :global(.areas) {
		margin-bottom: var(--space-sm);
	}
	.when {
		font-weight: var(--weight-strong);
		font-variant-numeric: tabular-nums;
	}
	.numbers,
	.hint {
		color: var(--muted);
		font-size: var(--text-sm);
	}
	dl {
		display: grid;
		grid-template-columns: max-content minmax(0, 1fr);
		gap: var(--space-2xs) var(--space-lg);
		margin: var(--space-sm) 0;
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
		overflow-wrap: anywhere;
	}
	.jobs {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2xs) var(--space-md);
	}
	@media (max-width: 47.99rem) {
		.event {
			margin: calc(-1 * var(--page-pad-phone));
		}
	}
</style>
