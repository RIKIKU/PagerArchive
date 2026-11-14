<script lang="ts" generics="T extends { from: string; to: string }">
	// R38a: what every report page has. Its filters, where they apply, in a
	// form whose values are kept in the page's address (bookmarks, Back);
	// the range calendar (R33a, M21); the report, run as the page opens with
	// the default range when none is set; a line when the counts are still
	// being made; the chart and the table beside it (below it on a phone);
	// and saving the table as CSV and the chart as an image.
	import type { Snippet } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, type Meta } from './api.ts';
	import { isoDate } from './format.ts';
	import { rangeProblem } from './filters.ts';
	import { canCopyImage, chartImage, copyImage } from './chartImage.ts';
	import { csvText, fileName, reportQuery, reportsApi, save, type Csv, type ReportName, type ReportOptions } from './reports.ts';
	import DateRange from './DateRange.svelte';
	import Icon from './Icon.svelte';

	let {
		title,
		intro,
		name,
		slug,
		defaultDays = 30,
		filters = { agency: true, eventType: true, priority: true },
		controls,
		body,
		csv,
		empty,
		emptyText = 'No jobs in this range.',
		prepare,
		stacked = false
	}: {
		title: string;
		intro: string;
		name: ReportName;
		slug: string;
		defaultDays?: number;
		filters?: { agency?: boolean; eventType?: boolean; priority?: boolean };
		/** The report's own controls, in the form (a limit, an alias). */
		controls?: Snippet<[URLSearchParams]>;
		/** The chart (in an element marked data-chart) and the table. */
		body: Snippet<[T, Meta]>;
		csv: (data: T, meta: Meta) => Csv;
		empty: (data: T) => boolean;
		emptyText?: string;
		/** Turns what the report's own controls hold into the address's
		 * parameters, or says why it can't ('' when it can). */
		prepare?: (form: FormData, params: URLSearchParams) => string;
		/** The chart above the table at every width, for a wide table. */
		stacked?: boolean;
	} = $props();

	let meta: Meta | undefined = $state();
	let options: ReportOptions | undefined = $state();
	let data: T | undefined = $state();
	let error = $state('');
	let problem = $state('');
	let saved = $state('');
	let loading = $state(false);
	let results: HTMLDivElement | undefined = $state();
	const copyable = typeof window !== 'undefined' && canCopyImage();

	api.meta().then((m) => (meta = m));
	reportsApi
		.options()
		.then((o) => (options = o))
		.catch(() => (options = { agencies: [], event_types: [], priorities: ['Emergency', 'Non-emergency', 'Admin'] }));

	const defaults = $derived(
		meta
			? { from: isoDate(new Date(Date.now() - (defaultDays - 1) * 864e5).toISOString(), meta.time_zone), to: isoDate(new Date().toISOString(), meta.time_zone) }
			: undefined
	);
	const query = $derived(defaults ? reportQuery(page.url.searchParams, defaults) : undefined);

	let asked = 0;
	$effect(() => {
		if (!query) return;
		const q = new URLSearchParams(query);
		const bad = rangeProblem(q.get('from'), q.get('to'));
		data = undefined;
		error = bad;
		saved = '';
		if (bad) return;
		const n = ++asked;
		loading = true;
		reportsApi
			.report<T>(name, q)
			.then((d) => {
				if (n === asked) data = d;
			})
			.catch((e) => {
				if (n === asked) error = e.message;
			})
			.finally(() => {
				if (n === asked) loading = false;
			});
	});

	function submit(ev: SubmitEvent) {
		ev.preventDefault();
		const form = new FormData(ev.currentTarget as HTMLFormElement);
		const params = new URLSearchParams();
		for (const [k, v] of form) if (typeof v === 'string' && v && !k.startsWith('_')) params.set(k, v);
		problem = prepare ? prepare(form, params) : '';
		if (problem) return;
		goto(`?${params}`, { reset: false });
	}

	function downloadCsv() {
		if (!data || !meta) return;
		save(`${fileName(slug, data.from, data.to)}.csv`, new Blob([csvText(csv(data, meta))], { type: 'text/csv;charset=utf-8' }));
		saved = 'The table was saved as a CSV file.';
	}

	function chartEl(): Element | undefined {
		return results?.querySelector('[data-chart]') ?? undefined;
	}

	async function downloadChart() {
		const el = chartEl();
		if (!el || !data) return;
		try {
			save(`${fileName(slug, data.from, data.to)}.png`, await chartImage(el));
			saved = 'The chart was saved as an image.';
		} catch (e) {
			saved = `The chart couldn't be saved: ${(e as Error).message}`;
		}
	}

	async function copyChart() {
		const el = chartEl();
		if (!el) return;
		try {
			await copyImage(el);
			saved = 'The chart was copied as an image.';
		} catch {
			saved = "The chart couldn't be copied. Save it as an image instead.";
		}
	}

	const value = (k: string) => query?.get(k) ?? '';
</script>

<svelte:head><title>{title} · PagerArchive</title></svelte:head>

<div class="page">
	<h1>{title}</h1>
	<p class="note">{intro}</p>

	{#if meta && query}
		<form method="GET" class="form" onsubmit={submit} data-testid="report-form">
			{#if filters.agency}
				<label class="field">
					<span>Agency</span>
					<select name="agency">
						<option value="">Every agency</option>
						{#each options?.agencies ?? [] as a (a)}<option value={a} selected={a === value('agency')}>{a}</option>{/each}
						{#if value('agency') && !(options?.agencies ?? []).includes(value('agency'))}<option value={value('agency')} selected>{value('agency')}</option>{/if}
					</select>
				</label>
			{/if}
			{#if filters.eventType}
				<label class="field">
					<span>Event type</span>
					<select name="event_type">
						<option value="">Every event type</option>
						{#each options?.event_types ?? [] as e (e.code)}<option value={e.code} selected={e.code === value('event_type')}>{e.label ? `${e.code} ${e.label}` : e.code}</option>{/each}
						{#if value('event_type') && !(options?.event_types ?? []).some((e) => e.code === value('event_type'))}<option value={value('event_type')} selected>{value('event_type')}</option>{/if}
					</select>
				</label>
			{/if}
			{#if filters.priority}
				<label class="field">
					<span>Priority</span>
					<select name="priority">
						<option value="">Every priority</option>
						{#each options?.priorities ?? ['Emergency', 'Non-emergency', 'Admin'] as p (p)}<option value={p} selected={p === value('priority')}>{p}</option>{/each}
					</select>
				</label>
			{/if}
			{@render controls?.(query)}
			<div class="dates">
				<span class="dates-label">Dates</span>
				<DateRange from={value('from')} to={value('to')} timeZone={meta.time_zone} firstPage={meta.first_page} lastPage={meta.last_page} />
			</div>
			<button type="submit">Show</button>
			{#if problem}<p class="problem" role="alert">{problem}</p>{/if}
		</form>
	{/if}

	{#if error}<p class="error" role="alert">{error}</p>{/if}
	<p class="status" role="status">
		{#if loading}Counting…{:else if data}{saved}{/if}
	</p>
	{#if data && meta}
		{@const d = data as T & { building?: boolean; pending?: number }}
		{#if d.building}
			<p class="warn" data-testid="report-building">The counts are being made for the first time, or for a new time zone: these numbers are incomplete until that's done.</p>
		{:else if (d.pending ?? 0) > 0}
			<p class="warn" data-testid="report-pending">{d.pending} recently changed {d.pending === 1 ? 'job is' : 'jobs are'} still being counted: show the report again in a few seconds.</p>
		{/if}
		{#if empty(data)}
			<p class="empty" data-testid="report-empty">{emptyText}</p>
		{:else}
			<div class="tools">
				<button type="button" onclick={downloadCsv}><Icon name="download" /> Download CSV</button>
				<button type="button" onclick={downloadChart}><Icon name="image" /> Download chart</button>
				{#if copyable}<button type="button" onclick={copyChart}><Icon name="copy" /> Copy chart</button>{/if}
			</div>
			<div class="results" class:stacked bind:this={results} data-testid="report">
				{@render body(data, meta)}
			</div>
		{/if}
	{/if}
</div>

<style>
	.page {
		padding: 0 0 var(--space-lg);
		min-width: 0;
	}
	h1 {
		font-size: var(--text-xl);
		margin: var(--space-md) 0 var(--space-2xs);
	}
	.note {
		color: var(--muted);
		font-size: var(--text-sm);
		margin-top: 0;
		max-width: var(--measure);
	}
	.form {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm) var(--space-md);
		align-items: flex-end;
		margin: var(--space-md) 0 var(--space-md);
	}
	.field,
	.dates {
		display: flex;
		flex-direction: column;
		gap: var(--space-3xs);
		font-size: var(--text-sm);
		min-width: 0;
	}
	.field > span,
	.dates-label {
		font-weight: var(--weight-strong);
	}
	.field select {
		min-height: var(--target);
		max-width: var(--field-wide);
	}
	.problem,
	.error {
		flex-basis: 100%;
		margin: 0;
		color: var(--danger-text);
		font-size: var(--text-sm);
	}
	.status {
		min-height: var(--leading);
		margin: 0;
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.warn {
		background: var(--warn-bg);
		color: var(--warn-fg);
		padding: var(--space-xs) var(--space-sm);
		border-radius: var(--radius-small);
		font-size: var(--text-sm);
		max-width: var(--measure);
	}
	.empty {
		color: var(--muted);
	}
	.tools {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		margin: var(--space-sm) 0 var(--space-md);
	}
	.tools button {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2xs);
	}
	.results {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: var(--space-lg);
		align-items: start;
	}
	@media (min-width: 1280px) {
		.results {
			grid-template-columns: minmax(0, 3fr) minmax(0, 2fr);
		}
		.results.stacked {
			grid-template-columns: minmax(0, 1fr);
		}
	}
	.results :global(table) {
		border-collapse: collapse;
		width: 100%;
	}
	.results :global(caption) {
		text-align: left;
		font-weight: var(--weight-strong);
		padding-bottom: var(--space-sm);
	}
	.results :global(th),
	.results :global(td) {
		text-align: left;
		padding: var(--space-xs) var(--space-sm) var(--space-xs) 0;
		border-bottom: var(--border-width) solid var(--border);
		font-weight: var(--weight-normal);
		vertical-align: top;
	}
	.results :global(thead th),
	.results :global(tfoot th),
	.results :global(tfoot td) {
		font-weight: var(--weight-strong);
	}
	.results :global(.num) {
		text-align: right;
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
	.results :global(.muted) {
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.results :global(.scroll) {
		overflow-x: auto;
		min-width: 0;
	}
</style>
