<script lang="ts">
	// R87c: large events, newest first: start and end (or ongoing), the
	// levels met, and how many deployment codes, jobs and distinct units their
	// pages hold. Every signed-in user sees them, as they see the pages.
	import { api, type LargeEvent, type Meta } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';
	import Icon from '#lib/Icon.svelte';
	import { span, numbers } from '#lib/largeEvents.ts';

	let meta: Meta | undefined = $state();
	let list: LargeEvent[] = $state([]);
	let next: string | undefined = $state();
	let error = $state('');
	let loaded = $state(false);
	let busy = $state(false);
	const tz = $derived(meta?.time_zone ?? 'UTC');

	api.meta().then((m) => (meta = m));
	async function load(before?: string) {
		busy = true;
		try {
			const p = await api.largeEvents(before);
			list = before ? [...list, ...p.events] : p.events;
			next = p.next;
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
			loaded = true;
		}
	}
	load();
</script>

<svelte:head><title>Large events · PagerArchive</title></svelte:head>

<h1>Large events</h1>
<p class="status">
	Times something big happened on the network: an Emergency page carrying a deployment code, or one job naming many distinct units. Pages
	within 12 hours of each other are grouped, so a busy day's incidents may share one event.
</p>
{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if loaded && list.length === 0 && !error}<p class="status" data-testid="no-events">No large events yet.</p>{/if}
{#if list.length && meta}
	<ol class="events" aria-label="Large events, newest first">
		{#each list as ev (ev.id)}
			<li data-testid="large-event" data-event-id={ev.id}>
				<a class="card" href="/large-events/{ev.id}">
					<span class="when">
						<span class="icon-box"><Icon name="largeevents" /></span>
						<span>
							<time datetime={isoAttr(ev.start)}>{dateTime(ev.start, tz)}</time>
							{#if ev.ongoing}<span class="ongoing">ongoing</span>{:else}to <time datetime={isoAttr(ev.end)}>{span(ev, tz)}</time>{/if}
						</span>
					</span>
					<span class="levels">
						{#each ev.levels as l (l.level)}
							<span class="level" title="Met {dateTime(l.at, tz)}">{l.label}</span>
						{/each}
					</span>
					<span class="numbers">{numbers(ev)}</span>
				</a>
			</li>
		{/each}
	</ol>
	{#if next}
		<button type="button" class="secondary" onclick={() => load(next)} disabled={busy}>Older events</button>
	{/if}
{/if}

<style>
	h1 {
		font-size: var(--text-xl);
		margin: var(--space-md) 0 var(--space-2xs);
	}
	.status {
		color: var(--muted);
		max-width: var(--measure);
	}
	.events {
		list-style: none;
		padding: 0;
		margin: var(--space-md) 0;
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		max-width: var(--measure-wide);
	}
	.card {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-xs) var(--space-lg);
		padding: var(--row-pad-y) var(--row-pad-x);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius);
		background: var(--surface);
		color: inherit;
		text-decoration: none;
		min-height: var(--target);
	}
	.card:hover {
		background: var(--hover);
	}
	.when {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		font-weight: var(--weight-strong);
		font-variant-numeric: tabular-nums;
		overflow-wrap: anywhere;
	}
	.icon-box {
		display: inline-grid;
		place-items: center;
		flex: none;
		width: var(--target);
		height: var(--target);
		border-radius: var(--radius-button);
		background: var(--accent-soft);
		color: var(--accent-soft-fg);
	}
	.ongoing {
		padding: var(--space-3xs) var(--space-xs);
		border-radius: var(--radius-badge);
		background: var(--accent-soft);
		color: var(--accent-soft-fg);
		font-size: var(--text-sm);
	}
	.levels {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-xs);
	}
	.level {
		padding: var(--space-3xs) var(--space-sm);
		border: var(--border-width) solid var(--control-border);
		border-radius: var(--radius-badge);
		font-size: var(--text-sm);
	}
	.numbers {
		color: var(--muted);
		font-size: var(--text-sm);
	}
</style>
