<script lang="ts">
	// R38a: the jobs behind a report's number (a cell of When it's busy, an
	// appliance of Appliance activity, R38d, R38h): exactly the jobs that
	// number counts, with the report's filters, newest first, 50 to a page.
	// Its address holds all of it, so it can be bookmarked and shared, and
	// the report it came from (report=) for the way back. Counts only: job
	// numbers, times, pages, event types and localities, never page text.
	import { page } from '$app/state';
	import { api, type Meta } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';
	import { groups } from '#lib/nav.ts';
	import ReportCaveat from '#lib/ReportCaveat.svelte';
	import { eventTypeName, jobsTitle, reportHref, reportsApi, type EventType, type JobsReport } from '#lib/reports.ts';

	let meta: Meta | undefined = $state();
	let eventTypes: EventType[] = $state([]);
	let data: JobsReport | undefined = $state();
	let error = $state('');
	let loading = $state(false);

	api.meta().then((m) => (meta = m));
	reportsApi
		.options()
		.then((o) => (eventTypes = o.event_types))
		.catch(() => {});

	// The report to go back to: one of the Reports group's, by its address.
	const back = $derived.by(() => {
		const name = page.url.searchParams.get('report') ?? '';
		const entry = groups.find((g) => g.id === 'reports')?.entries.find((e) => e.href === `/reports/${name}`);
		return entry && /^[a-z-]+$/.test(name) ? { label: entry.label, href: reportHref(name, page.url.searchParams) } : { label: 'Reports', href: '/reports' };
	});

	// What the API is asked: everything in the address but the way back.
	const queryText = $derived.by(() => {
		const q = new URLSearchParams(page.url.search);
		q.delete('report');
		return q.toString();
	});

	let asked = 0;
	$effect(() => {
		const q = new URLSearchParams(queryText);
		const n = ++asked;
		data = undefined;
		error = '';
		loading = true;
		reportsApi
			.report<JobsReport>('jobs', q)
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

	const title = $derived(data ? jobsTitle(data, eventTypes) : 'Jobs');
	const pages = $derived(data ? Math.max(1, Math.ceil(data.jobs / data.page_size)) : 1);
	function pageHref(n: number): string {
		const q = new URLSearchParams(page.url.search);
		if (n === 1) q.delete('page');
		else q.set('page', String(n));
		return `?${q}`;
	}
	const firstShown = $derived(data ? (data.page - 1) * data.page_size + 1 : 0);
</script>

<svelte:head><title>Jobs · PagerArchive</title></svelte:head>

<div class="page">
	<p class="back"><a href={back.href} data-testid="jobs-back">Back to {back.label}</a></p>
	<h1 data-testid="jobs-title">{title}</h1>
	<p class="note">The jobs that make up the report's number: a job number's Emergency pages are one job, counted at its first page's sent time, in the deployment's time zone.</p>
	<ReportCaveat />

	{#if error}<p class="error" role="alert">{error}</p>{/if}
	<p class="status" role="status">{#if loading}Counting…{/if}</p>
	{#if data && meta}
		{#if data.building}
			<p class="warn" data-testid="report-building">The counts are being made for the first time, or for a new time zone: these numbers are incomplete until that's done.</p>
		{:else if data.pending > 0}
			<p class="warn" data-testid="report-pending">{data.pending} recently changed {data.pending === 1 ? 'job is' : 'jobs are'} still being counted: show the list again in a few seconds.</p>
		{/if}
		{#if data.jobs === 0}
			<p class="empty" data-testid="report-empty">No jobs.</p>
		{:else if data.rows.length === 0}
			<p class="empty" data-testid="report-empty">There are only {data.jobs} {data.jobs === 1 ? 'job' : 'jobs'}: <a href={pageHref(1)}>see the first {data.page_size}</a>.</p>
		{:else}
			<!-- svelte-ignore a11y_no_noninteractive_tabindex (a table wider than the screen scrolls in its box, which the keyboard reaches too: WCAG 2.1.1) -->
			<div class="scroll" tabindex="0" role="region" aria-label="Jobs">
				<table data-testid="jobs-table">
					<caption>{data.jobs === 1 ? '1 job' : `${data.jobs} jobs`}{data.jobs > data.rows.length ? `, ${firstShown} to ${firstShown + data.rows.length - 1} shown` : ''}, newest first</caption>
					<thead>
						<tr><th scope="col">Job</th><th scope="col">First page</th><th scope="col" class="num">Pages</th></tr>
					</thead>
					<tbody>
						{#each data.rows as j (j.job_number)}
							<tr>
								<th scope="row"><a href="/jobs/{encodeURIComponent(j.job_number)}">{j.job_number}</a><br /><span class="muted">{eventTypeName(j.event_type)}{j.locality ? `, ${j.locality}` : ''}</span></th>
								<td><time datetime={isoAttr(j.first)}>{dateTime(j.first, meta.time_zone)}</time></td>
								<td class="num">{j.pages}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
			{#if pages > 1}
				<nav class="pager" aria-label="Pages of jobs">
					{#if data.page > 1}<a href={pageHref(data.page - 1)} rel="prev">Newer {data.page_size}</a>{/if}
					<span>Page {data.page} of {pages}</span>
					{#if data.page < pages}<a href={pageHref(data.page + 1)} rel="next">Older {Math.min(data.page_size, data.jobs - data.page * data.page_size)}</a>{/if}
				</nav>
			{/if}
		{/if}
	{/if}
</div>

<style>
	.page {
		padding: 0 0 var(--space-lg);
		min-width: 0;
	}
	.back {
		margin: var(--space-md) 0 0;
	}
	.back a,
	.pager a {
		display: inline-flex;
		align-items: center;
		min-height: var(--target);
	}
	h1 {
		font-size: var(--text-xl);
		margin: var(--space-2xs) 0;
		overflow-wrap: anywhere;
	}
	.note {
		color: var(--muted);
		font-size: var(--text-sm);
		margin-top: 0;
		max-width: var(--measure);
	}
	.error {
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
	.scroll {
		overflow-x: auto;
		min-width: 0;
		max-width: var(--measure);
	}
	table {
		border-collapse: collapse;
		width: 100%;
	}
	caption {
		text-align: left;
		font-weight: var(--weight-strong);
		padding-bottom: var(--space-sm);
	}
	th,
	td {
		text-align: left;
		padding: var(--space-xs) var(--space-sm) var(--space-xs) 0;
		border-bottom: var(--border-width) solid var(--border);
		font-weight: var(--weight-normal);
		vertical-align: top;
	}
	thead th {
		font-weight: var(--weight-strong);
	}
	.num {
		text-align: right;
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
	.muted {
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.pager {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-sm) var(--space-md);
		margin-top: var(--space-sm);
		font-size: var(--text-sm);
	}
</style>
