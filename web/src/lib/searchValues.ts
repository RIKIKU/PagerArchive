// R33a, M21: the values the search box suggests for alias:, agency: and
// group:, asked for once and matched in the browser, so typing makes no
// requests however many aliases there are (ADR 0023). They're asked for
// again when the box is used after a minute, to pick up new aliases.
import { api } from './api.ts';

export type SearchValues = Awaited<ReturnType<typeof api.searchValues>>;

const fresh = 60_000;
let asked: { at: number; values: Promise<SearchValues> } | undefined;

export function searchValues(): Promise<SearchValues> {
	if (!asked || Date.now() - asked.at > fresh) {
		const values = api.searchValues();
		const mine = { at: Date.now(), values };
		asked = mine;
		// A failure is asked again next time.
		values.catch(() => {
			if (asked === mine) asked = undefined;
		});
	}
	return asked.values;
}
