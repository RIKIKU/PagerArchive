// Times are stored in UTC and shown in the deployment's time zone with
// en-AU style dates (R35a).

const cache = new Map<string, Intl.DateTimeFormat>();

function fmt(timeZone: string, opts: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
	const key = timeZone + JSON.stringify(opts);
	let f = cache.get(key);
	if (!f) {
		f = new Intl.DateTimeFormat('en-AU', { timeZone, ...opts });
		cache.set(key, f);
	}
	return f;
}

/** 9 Oct 2026 */
export function date(iso: string, timeZone: string): string {
	return fmt(timeZone, { day: 'numeric', month: 'short', year: 'numeric' }).format(new Date(iso));
}

/** 14:05:09 */
export function time(iso: string, timeZone: string): string {
	return fmt(timeZone, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }).format(
		new Date(iso)
	);
}

/** 9 Oct 2026, 14:05:09 */
export function dateTime(iso: string, timeZone: string): string {
	return `${date(iso, timeZone)}, ${time(iso, timeZone)}`;
}

/** Machine-readable value for <time datetime>. */
export function isoAttr(iso: string): string {
	return new Date(iso).toISOString();
}

/** 2026-10-09: the date in timeZone, as date inputs and the API take it. */
export function isoDate(iso: string, timeZone: string): string {
	const parts: Record<string, string> = {};
	for (const p of fmt(timeZone, { year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(new Date(iso)))
		parts[p.type] = p.value;
	return `${parts.year}-${parts.month}-${parts.day}`;
}
