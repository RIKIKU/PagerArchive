// From and To dates, for a search (search.ts) or a report.

/** Why a From/To date range can't be used ('' when it can). The API refuses a
 *  From after the To (reports) or finds nothing (search), so the browser
 *  stops it first. Dates are YYYY-MM-DD, which compare as strings. */
export function rangeProblem(from: string | null, to: string | null): string {
	return from && to && from > to ? 'The To date is before the From date. Choose a To date on or after the From date.' : '';
}
