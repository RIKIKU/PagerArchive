// Search criteria (R33), kept in the page URL so a search can be shared and
// survives a reload. Keys match the API's query parameters.

export const textKeys = ['q', 'capcode', 'alias', 'agency', 'group', 'job', 'from', 'to'] as const;
export const choiceKeys = ['priority', 'category'] as const;
export const boolKeys = ['unaliased', 'ignored'] as const;
export const adminBoolKeys = ['parse_failed', 'address_unsplit', 'location_mismatch'] as const;

const allKeys = [...textKeys, ...choiceKeys, ...boolKeys, ...adminBoolKeys];

/** The search criteria in a URL, without unrelated parameters. */
export function criteria(url: { searchParams: { get(key: string): string | null } }): URLSearchParams {
	const out = new URLSearchParams();
	for (const k of allKeys) {
		const v = url.searchParams.get(k);
		if (v) out.set(k, v);
	}
	return out;
}

export function isFiltered(q: URLSearchParams): boolean {
	return [...q.keys()].length > 0;
}

export const labels: Record<string, string> = {
	q: 'Text',
	capcode: 'Capcode',
	alias: 'Alias',
	agency: 'Agency',
	group: 'Group',
	job: 'Job number',
	from: 'From',
	to: 'To',
	priority: 'Priority',
	category: 'Event category',
	unaliased: 'Capcodes with no alias',
	ignored: 'Include ignored aliases',
	parse_failed: 'Parse failed',
	address_unsplit: 'Address unsplit',
	location_mismatch: 'Location mismatch'
};

/** Why a From/To date range can't be used ('' when it can). The API refuses a
 *  From after the To (reports) or finds nothing (search), so the browser
 *  stops it first. Dates are YYYY-MM-DD, which compare as strings. */
export function rangeProblem(from: string | null, to: string | null): string {
	return from && to && from > to ? 'The To date is before the From date. Choose a To date on or after the From date.' : '';
}
