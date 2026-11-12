// R87c: how a large event reads in the list and its view.
import type { LargeEvent } from './api.ts';
import { date, dateTime, time } from './format.ts';

const count = (n: number, one: string, many: string) => `${n.toLocaleString('en-AU')} ${n === 1 ? one : many}`;

/** Its end: the last page's time, with the date only when it's another day. */
export function span(ev: LargeEvent, tz: string): string {
	return date(ev.end, tz) === date(ev.start, tz) ? time(ev.end, tz) : dateTime(ev.end, tz);
}

/** "2 deployment codes · 2 jobs · 14 units · 5 pages", from the pages it still holds (R87e). */
export function numbers(ev: LargeEvent): string {
	return [
		count(ev.deployment_codes, 'deployment code', 'deployment codes'),
		count(ev.jobs, 'job', 'jobs'),
		count(ev.units, 'unit', 'units'),
		count(ev.pages, 'page', 'pages')
	].join(' · ');
}
