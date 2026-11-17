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
	jobs: number;
	pending: number;
	building: boolean;
}

export interface ReportOptions {
	agencies: string[];
	event_types: EventType[];
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

/** The jobs behind a report's number (R38a): its filters, what the number
 * is of, how many jobs (jobs) and one page of them, newest first. */
export interface JobsReport extends ReportBase {
	alias?: string;
	capcode?: string;
	/** 1 Monday to 7 Sunday. */
	weekday?: number;
	hour?: number;
	appliance?: string;
	page: number;
	page_size: number;
	rows: { job_number: string; first: string; pages: number; event_type: EventType; locality: string | null }[];
}

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
	report: <T>(name: ReportName | 'jobs', query: URLSearchParams) => get<T>(`/api/v1/reports/${name}?${query}`)
};

/** The filters a report's address may hold (R38a), in the order they're written. */
export const filterKeys = ['from', 'to', 'agency', 'event_type', 'alias', 'capcode', 'limit', 'by'] as const;

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

/** The filters a report's number keeps when it links to the jobs behind it (R38a). */
const jobFilterKeys = ['from', 'to', 'agency', 'event_type', 'alias', 'capcode'] as const;

/** What a report's number is of, for the jobs behind it: a weekday (1
 * Monday to 7 Sunday) and hour, or an appliance. */
export interface JobsCut {
	weekday?: number;
	hour?: number;
	appliance?: string;
}

/** The address of the jobs behind a report's number (R38a): the report's
 * filters, as it was answered, what the number is of, and the report to go
 * back to. */
export function jobsHref(report: ReportName, d: ReportBase & { alias?: string; capcode?: string }, cut: JobsCut): string {
	const q = new URLSearchParams();
	for (const k of jobFilterKeys) {
		const v = d[k];
		if (v) q.set(k, v);
	}
	if (cut.weekday !== undefined) q.set('weekday', String(cut.weekday));
	if (cut.hour !== undefined) q.set('hour', String(cut.hour));
	if (cut.appliance) q.set('appliance', cut.appliance);
	q.set('report', report);
	return `/reports/jobs?${q}`;
}

/** The report's address, with the filters the jobs behind its number were
 * listed with (R38a: the way back). */
export function reportHref(report: string, search: { get(name: string): string | null }): string {
	const q = new URLSearchParams();
	for (const k of jobFilterKeys) {
		const v = search.get(k);
		if (v) q.set(k, v);
	}
	return `/reports/${report}?${q}`;
}

export const weekdays = ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday'];

/** 14:00: an hour of the day. */
export const hourName = (h: number) => `${String(h).padStart(2, '0')}:00`;

/** 5 to 11 Oct 2026, 28 Sep to 4 Oct 2026, 29 Dec 2025 to 4 Jan 2026, or
 * 5 Oct 2026: a range of YYYY-MM-DD days. */
export function rangeName(from: string, to: string): string {
	if (from === to) return shortDay(from);
	const [a, b] = [shortDay(from), shortDay(to)];
	if (from.slice(0, 4) !== to.slice(0, 4)) return `${a} to ${b}`;
	if (from.slice(0, 7) !== to.slice(0, 7)) return `${a.replace(/ \d{4}$/, '')} to ${b}`;
	return `${a.replace(/ \S+ \d{4}$/, '')} to ${b}`;
}

/** What the jobs behind a number are, with the range and filters: "Jobs on
 * Saturdays 14:00–14:59, 5 to 11 Oct 2026, Example Fire Service". */
export function jobsTitle(d: JobsReport, eventTypes: EventType[]): string {
	let s = 'Jobs';
	if (d.alias) s += ` to ${d.alias}`;
	else if (d.capcode) s += ` to capcode ${d.capcode}`;
	if (d.weekday) s += ` on ${weekdays[d.weekday - 1]}s`;
	if (d.hour !== undefined) s += `${d.weekday ? '' : ' at'} ${hourName(d.hour)}–${String(d.hour).padStart(2, '0')}:59`;
	if (d.appliance) s += ` naming ${d.appliance}`;
	s += `, ${rangeName(d.from, d.to)}`;
	if (d.agency) s += `, ${d.agency}`;
	if (d.event_type) s += `, ${eventTypeName(eventTypes.find((e) => e.code === d.event_type) ?? { code: d.event_type })}`;
	return s;
}
