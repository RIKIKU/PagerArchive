// M24's reports (R38a-R38i, ADR 0031): their API's types and calls, their
// filters in the page's address, and their CSV. Counts only, never page text.
import { ApiError, signIn } from './api.ts';

export interface EventType {
	/** null: jobs with no event type. */
	code: string | null;
	label?: string;
	category?: string;
}

export interface ReportBase {
	from: string;
	to: string;
	agency?: string;
	event_type?: string;
	priority?: string;
	jobs: number;
	pending: number;
	building: boolean;
}

export interface ReportOptions {
	agencies: string[];
	event_types: EventType[];
	priorities: string[];
}

export interface BusiestRow {
	rank: number;
	alias_id: number | null;
	alias?: string;
	pattern?: string;
	capcode?: string;
	agency?: string;
	jobs: number;
	share: number;
	previous_jobs: number;
}

export interface BusiestReport extends ReportBase {
	limit: number;
	previous_from: string;
	previous_to: string;
	previous_jobs: number;
	rows: BusiestRow[];
}

export interface LocalityRow {
	locality: string | null;
	jobs: number;
	share: number;
	lon?: number;
	lat?: number;
}

export interface LocalitiesReport extends ReportBase {
	rows: LocalityRow[];
}

export interface BusyTimesReport extends ReportBase {
	alias?: string;
	capcode?: string;
	aliases?: { id: number; pattern: string }[];
	/** cells[d][h]: jobs that started on weekday d (0 Monday) in hour h. */
	cells: number[][];
}

export interface EventTypesReport extends ReportBase {
	types: (EventType & { jobs: number; share: number })[];
	/** Each month's jobs, and each event type's, in the order of types. */
	months: { month: string; jobs: number; types: number[] }[];
}

export interface SizeStat {
	median: number;
	low: number;
	high: number;
	max: number;
}

export interface JobSizeReport extends ReportBase {
	rows: { event_type: EventType | null; jobs: number; pages: SizeStat; capcodes: SizeStat; appliances: SizeStat }[];
}

export interface LongJobsReport extends ReportBase {
	limit: number;
	rows: { job_number: string; first: string; last: string; seconds: number; pages: number; event_type: EventType; locality: string | null }[];
}

export interface AppliancesReport extends ReportBase {
	rows: { appliance: string; jobs: number; share: number }[];
}

export interface LargeEventsReport {
	from: string;
	to: string;
	by: 'week' | 'month';
	events: number;
	median_seconds: number | null;
	median_jobs: number | null;
	buckets: { start: string; events: number; median_seconds: number | null; median_jobs: number | null }[];
}

export type ReportName = 'busiest' | 'localities' | 'busy-times' | 'event-types' | 'job-size' | 'long-jobs' | 'appliances' | 'large-events';

async function get<T>(path: string): Promise<T> {
	const res = await fetch(path, { headers: { Accept: 'application/json' } });
	if (!res.ok) {
		let msg = `${res.status} ${res.statusText}`;
		try {
			const b = await res.json();
			if (b?.error) msg = b.error;
		} catch {
			// not JSON
		}
		if (res.status === 401) signIn();
		throw new ApiError(res.status, msg);
	}
	return res.json() as Promise<T>;
}

let optionsPromise: Promise<ReportOptions> | undefined;

export const reportsApi = {
	options: () => (optionsPromise ??= get<ReportOptions>('/api/v1/reports/options').catch((e) => {
		optionsPromise = undefined;
		throw e;
	})),
	report: <T>(name: ReportName, query: URLSearchParams) => get<T>(`/api/v1/reports/${name}?${query}`)
};

/** The filters a report's address may hold (R38a), in the order they're written. */
export const filterKeys = ['from', 'to', 'agency', 'event_type', 'priority', 'alias', 'capcode', 'limit', 'by'] as const;

/** The report's query from its page's address: what's set there, with the
 * default range (the last `days` days, in the deployment's time zone) when
 * none is. */
export function reportQuery(search: { get(name: string): string | null }, defaults: { from: string; to: string }): URLSearchParams {
	const q = new URLSearchParams();
	for (const k of filterKeys) {
		const v = search.get(k);
		if (v) q.set(k, v);
	}
	if (!q.get('from') || !q.get('to')) {
		q.set('from', defaults.from);
		q.set('to', defaults.to);
	}
	return q;
}

/** 37%: a share, rounded to whole percent ("<1%" for a few). */
export function percent(share: number): string {
	if (share > 0 && share < 0.005) return '<1%';
	return `${Math.round(share * 100)}%`;
}

/** 2 h 30 min, 45 min, 30 s, 3 d 4 h: a span of time. */
export function span(seconds: number): string {
	if (seconds < 60) return `${seconds} s`;
	const m = Math.round(seconds / 60);
	if (m < 60) return `${m} min`;
	const h = Math.floor(m / 60);
	if (h < 48) return m % 60 ? `${h} h ${m % 60} min` : `${h} h`;
	const d = Math.floor(h / 24);
	return h % 24 ? `${d} d ${h % 24} h` : `${d} d`;
}

/** An event type as reports show it: "HSF1 House fire", or "No event type". */
export function eventTypeName(e: EventType | null | undefined): string {
	if (!e || e.code === null) return 'No event type';
	return e.label ? `${e.code} ${e.label}` : e.code;
}

/** 5 October 2026, from a YYYY-MM-DD day. */
export function longDay(d: string): string {
	const [y, m, day] = d.split('-').map(Number);
	return new Intl.DateTimeFormat('en-AU', { day: 'numeric', month: 'long', year: 'numeric', timeZone: 'UTC' }).format(new Date(Date.UTC(y, m - 1, day)));
}

/** Oct 2026, from YYYY-MM or YYYY-MM-DD. */
export function monthName(d: string): string {
	const [y, m] = d.split('-').map(Number);
	return new Intl.DateTimeFormat('en-AU', { month: 'short', year: 'numeric', timeZone: 'UTC' }).format(new Date(Date.UTC(y, m - 1, 1)));
}

/** 5 Oct 2026, from a YYYY-MM-DD day. */
export function shortDay(d: string): string {
	const [y, m, day] = d.split('-').map(Number);
	return new Intl.DateTimeFormat('en-AU', { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' }).format(new Date(Date.UTC(y, m - 1, day)));
}

export interface Csv {
	headers: string[];
	rows: (string | number | null)[][];
}

/** One CSV field: quoted when it needs to be, and a text starting with a
 * formula character prefixed with ' so a spreadsheet shows it as text. */
function field(v: string | number | null): string {
	if (v === null) return '';
	let s = String(v);
	if (typeof v === 'string' && /^[=+\-@\t\r]/.test(s)) s = `'${s}`;
	return /[",\n\r]/.test(s) ? `"${s.replaceAll('"', '""')}"` : s;
}

/** The CSV text: a header line, then one line per row, CRLF line ends. */
export function csvText(csv: Csv): string {
	return [csv.headers, ...csv.rows].map((r) => r.map(field).join(',')).join('\r\n') + '\r\n';
}

/** Saves a file in the browser (a download). */
export function save(name: string, blob: Blob) {
	const url = URL.createObjectURL(blob);
	const a = document.createElement('a');
	a.href = url;
	a.download = name;
	a.hidden = true;
	document.body.append(a);
	a.click();
	a.remove();
	setTimeout(() => URL.revokeObjectURL(url), 10_000);
}

/** pagerarchive-busiest-capcodes-2026-10-05-to-2026-10-11 */
export function fileName(slug: string, from: string, to: string): string {
	return `pagerarchive-${slug}-${from}-to-${to}`;
}
