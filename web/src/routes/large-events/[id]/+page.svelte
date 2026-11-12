<script lang="ts">
	// R87c: one large event: when it started and ended (or that it's
	// ongoing), the levels it met and when, its deployment codes and jobs, and
	// then its pages like the message list (P1, P2: the full text, as stored),
	// with the scrubber over its days (R81b) and live updates as pages join.
	import { page } from '$app/state';
	import { api, type LargeEvent, type Meta } from '#lib/api.ts';
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
				error = '';
			})
			.catch((e) => {
				ev = undefined;
				error = e.status === 404 ? 'There’s no such large event. Retention may have deleted its pages.' : e.message;
			});
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
		<MessageList {query} base={{ event: String(ev.id), ignored: 'true' }} title={title}>
			<div class="about" data-testid="event-about">
				<p class="when">
					<time datetime={isoAttr(ev.start)}>{dateTime(ev.start, tz)}</time>
					{#if ev.ongoing}<span class="ongoing">ongoing</span>{:else}to <time datetime={isoAttr(ev.end)}>{span(ev, tz)}</time>{/if}
				</p>
				<p class="numbers">{numbers(ev)}</p>
				<dl>
					<dt>Levels</dt>
					<dd>
						<ul class="levels">
							{#each ev.levels as l (l.level)}
								<li><span class="level">{l.label}</span> <time datetime={isoAttr(l.at)}>{dateTime(l.at, tz)}</time></li>
							{/each}
						</ul>
					</dd>
					{#if ev.deployment_code_list?.length}
						<dt>Deployment codes</dt>
						<dd data-testid="event-codes">{ev.deployment_code_list.join(', ')}</dd>
					{/if}
					{#if ev.job_list?.length}
						<dt>Jobs</dt>
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
	.when {
		font-weight: var(--weight-strong);
		font-variant-numeric: tabular-nums;
	}
	.ongoing {
		padding: var(--space-3xs) var(--space-xs);
		border-radius: var(--radius-badge);
		background: var(--accent-soft);
		color: var(--accent-soft-fg);
		font-size: var(--text-sm);
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
	.levels {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2xs) var(--space-lg);
	}
	.level {
		padding: var(--space-3xs) var(--space-sm);
		border: var(--border-width) solid var(--control-border);
		border-radius: var(--radius-badge);
		font-size: var(--text-sm);
	}
	.levels time {
		color: var(--muted);
		font-size: var(--text-sm);
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
