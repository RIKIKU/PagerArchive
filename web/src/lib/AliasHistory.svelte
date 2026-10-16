<script lang="ts">
	// R57: an alias's changes, newest first, for admins only (owner,
	// 2026-10-16). Entries of deleted aliases that had the same capcode or
	// pattern are listed after them.
	import type { HistoryEntry } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';

	let { history, tz, level = 2 }: { history: HistoryEntry[]; tz: string; level?: 2 | 3 } = $props();

	const own = $derived(history.filter((h) => !h.earlier));
	const earlier = $derived(history.filter((h) => h.earlier));

	const labels: Record<string, string> = { pattern: 'Capcode or pattern', name: 'Name', template: 'Template', ignored: 'Ignored' };
	const order = ['pattern', 'name', 'template', 'ignored'];

	function show(v: unknown): string {
		if (v === null || v === undefined) return 'none';
		if (typeof v === 'boolean') return v ? 'yes' : 'no';
		return `“${String(v)}”`;
	}

	function lines(h: HistoryEntry): string[] {
		const fields = order.filter((f) => f in h.changes);
		if (h.action === 'added') return fields.map((f) => `${labels[f]}: ${show(h.changes[f][1])}`);
		if (h.action === 'deleted') return [];
		return fields.map((f) => `${labels[f]}: ${show(h.changes[f][0])} → ${show(h.changes[f][1])}`);
	}

	const verb = { added: 'Added', changed: 'Changed', deleted: 'Deleted' };

	function who(h: HistoryEntry): string {
		if (h.source === 'cli') return 'with the command line';
		if (h.source === 'import') return 'by import';
		if (h.source === 'request') return `requested by ${h.requested_by ?? 'deleted user'}, approved by ${h.made_by}`;
		return `by ${h.made_by}`;
	}
</script>

{#snippet entries(list: HistoryEntry[])}
	<ol class="history">
		{#each list as h (h.id)}
			<li>
				<div class="what">
					<strong>{verb[h.action]}</strong>
					{#if h.earlier}<span class="was">“{h.alias_name}” ({h.pattern})</span>{/if}
				</div>
				{#if lines(h).length}
					<ul class="fields">
						{#each lines(h) as l (l)}<li>{l}</li>{/each}
					</ul>
				{/if}
				<div class="by">{who(h)} · <time datetime={isoAttr(h.at)}>{dateTime(h.at, tz)}</time></div>
			</li>
		{/each}
	</ol>
{/snippet}

<section class="alias-history" aria-labelledby="history-h" data-testid="alias-history">
	<svelte:element this={`h${level}`} id="history-h">History</svelte:element>
	<p class="note">Only admins see this.</p>
	{#if own.length === 0}
		<p class="note">No changes recorded yet. Changes made before this version of PagerArchive weren’t recorded.</p>
	{:else}
		{@render entries(own)}
	{/if}
	{#if earlier.length}
		<svelte:element this={`h${level + 1}`}>Earlier aliases for this capcode or pattern</svelte:element>
		{@render entries(earlier)}
	{/if}
</section>

<style>
	.alias-history {
		padding: 0 0.75rem 0.5rem;
		max-width: 44rem;
	}
	.alias-history :global(h2),
	.alias-history :global(h3),
	.alias-history :global(h4) {
		font-size: var(--text-lg);
		margin: 1rem 0 0.25rem;
	}
	.note {
		color: var(--muted);
		font-size: var(--text-sm);
		margin: 0.25rem 0;
	}
	.history {
		list-style: none;
		padding: 0;
		margin: 0;
	}
	.history > li {
		padding: 0.5rem 0;
		border-bottom: 1px solid var(--border);
	}
	.was {
		color: var(--muted);
		overflow-wrap: anywhere;
	}
	.fields {
		margin: 0.2rem 0;
		padding-left: 1.25rem;
		overflow-wrap: anywhere;
	}
	.by {
		color: var(--muted);
		font-size: var(--text-sm);
		overflow-wrap: anywhere;
	}
</style>
