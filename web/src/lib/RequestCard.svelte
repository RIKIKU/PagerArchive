<script lang="ts">
	// One alias request (R54-R56): what's asked, the current value beside the
	// proposed one (R55), its marks and its status. Actions come from the page.
	import type { Snippet } from 'svelte';
	import type { AliasRequest } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';

	let { r, tz, actions, level = 2 }: { r: AliasRequest; tz: string; actions?: Snippet; level?: 2 | 3 } = $props();

	const statusText: Record<string, string> = {
		waiting: 'Waiting',
		approved: 'Approved',
		rejected: 'Rejected',
		withdrawn: 'Withdrawn',
		alias_deleted: 'Closed: alias deleted'
	};

	const name = $derived(r.current?.name ?? r.alias_name ?? '');
	const title = $derived(
		r.kind === 'add'
			? `New alias for ${r.pattern}`
			: r.kind === 'delete'
				? `Delete “${name}” (${r.pattern})`
				: `Change “${name}” (${r.pattern})`
	);

	const tpl = (t: string | null) => t ?? 'None (text only)';
	const yesNo = (b: boolean) => (b ? 'Yes' : 'No');

	// Rows of the Current | Proposed table; changed rows are marked.
	const rows = $derived.by(() => {
		const c = r.current;
		const p = r.proposed;
		if (r.kind === 'add')
			return [
				{ field: 'Name', cur: '', next: p.name ?? '', changed: true },
				{ field: 'Template', cur: '', next: tpl(p.template), changed: true },
				{ field: 'Ignored', cur: '', next: yesNo(p.ignored ?? false), changed: true }
			];
		if (r.kind === 'delete')
			return [{ field: 'Alias', cur: c ? `“${c.name}”` : '', next: 'Deleted', changed: true }];
		return [
			{ field: 'Name', cur: c?.name ?? '', next: p.name ?? '', changed: p.name !== null },
			{ field: 'Template', cur: c ? tpl(c.template) : '', next: p.set_template ? tpl(p.template) : '', changed: p.set_template },
			{ field: 'Ignored', cur: c ? yesNo(c.ignored) : '', next: p.ignored === null ? '' : yesNo(p.ignored), changed: p.ignored !== null }
		];
	});
</script>

<article class="request" data-testid="alias-request" data-status={r.status}>
	<div class="head">
		<svelte:element this={`h${level}`} class="title">
			{#if r.alias_id}<a href="/aliases/{r.alias_id}">{title}</a>{:else}{title}{/if}
		</svelte:element>
		<span class="tag" class:warn={r.status === 'rejected'} data-testid="request-status">{statusText[r.status]}</span>
		{#if r.changed_since_requested}<span class="tag warn">Changed since requested</span>{/if}
	</div>
	{#if r.now_aliased_as}
		<p class="mark">{r.pattern} now has the alias “{r.now_aliased_as}”: this request can't be approved.</p>
	{/if}
	<table>
		<caption class="visually-hidden">Current and proposed values</caption>
		<thead><tr><th scope="col">Field</th><th scope="col">Current</th><th scope="col">Proposed</th></tr></thead>
		<tbody>
			{#each rows as row (row.field)}
				<tr class:changed={row.changed}>
					<th scope="row">{row.field}</th>
					<td>{row.cur || '—'}</td>
					<td>{row.changed ? row.next : 'Unchanged'}</td>
				</tr>
			{/each}
		</tbody>
	</table>
	{#if r.note}<p class="text"><span class="label">Note:</span> {r.note}</p>{/if}
	{#if r.feedback}<p class="text feedback"><span class="label">Feedback:</span> {r.feedback}</p>{/if}
	<p class="meta">
		{#if r.requested_by}Requested by {r.requested_by} ·{/if}
		<time datetime={isoAttr(r.created_at)}>{dateTime(r.created_at, tz)}</time>
		{#if r.updated_at !== r.created_at && r.status === 'waiting'}· edited <time datetime={isoAttr(r.updated_at)}>{dateTime(r.updated_at, tz)}</time>{/if}
		{#if r.closed_at}· {statusText[r.status].toLowerCase()} <time datetime={isoAttr(r.closed_at)}>{dateTime(r.closed_at, tz)}</time>{/if}
		{#if r.reviewed_by}by {r.reviewed_by}{/if}
	</p>
	{#if actions}<div class="actions">{@render actions()}</div>{/if}
</article>

<style>
	.request {
		padding: 0.6rem 0;
		border-bottom: 1px solid var(--border);
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem 0.5rem;
		align-items: center;
	}
	.title {
		font-size: var(--text-base);
		margin: 0;
		overflow-wrap: anywhere;
	}
	.tag {
		font-size: var(--text-xs);
		border: 1px solid var(--border);
		border-radius: 1rem;
		padding: 0 0.5rem;
	}
	.tag.warn,
	.mark {
		background: var(--warn-bg);
		color: var(--warn-fg);
		border-color: var(--warn-fg);
	}
	.mark {
		padding: 0.25rem 0.5rem;
		border-radius: 0.25rem;
		margin: 0.4rem 0;
		font-size: var(--text-sm);
	}
	table {
		border-collapse: collapse;
		margin: 0.4rem 0;
		width: 100%;
		max-width: 36rem;
		font-size: var(--text-md);
	}
	th,
	td {
		text-align: left;
		padding: 0.2rem 0.5rem 0.2rem 0;
		vertical-align: top;
		overflow-wrap: anywhere;
	}
	thead th {
		color: var(--muted);
		font-weight: 600;
		border-bottom: 1px solid var(--border);
	}
	tbody th {
		font-weight: normal;
		color: var(--muted);
	}
	tr.changed td:last-child {
		font-weight: 600;
		background: var(--highlight);
	}
	.text {
		margin: 0.25rem 0;
		overflow-wrap: anywhere;
		white-space: pre-line;
	}
	.label {
		color: var(--muted);
	}
	.feedback {
		border-left: 3px solid var(--accent);
		padding-left: 0.5rem;
	}
	.meta {
		color: var(--muted);
		font-size: var(--text-sm);
		margin: 0.25rem 0;
		overflow-wrap: anywhere;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem;
		align-items: flex-start;
		margin-top: 0.4rem;
	}
</style>
