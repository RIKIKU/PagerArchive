// R81a: coming back to the message list (the back button, a breadcrumb or
// the menu) restores its search, date range and scroll position. The search
// is in the address; the links back to the list carry the last one. What the
// list had loaded, and where it was scrolled to, are kept here, in memory
// only: message text is never written to the browser's storage (P1, A60).
import type { Message } from './api.ts';

/** The message list's last search, as an address query ('' for none). */
export const list = $state({ query: '' });

/** The address of the message list as it was last left. */
export const listHref = () => (list.query ? `/?${list.query}` : '/');

export interface Kept {
	/** The search the list showed (its API parameters). */
	key: string;
	messages: Message[];
	next?: string;
	/** Where it was: the page's scroll, and the first row in view (its ID)
	 * and that row's distance from the top of the window. */
	place: { scrollY: number; row?: number; top: number };
}

let kept: Kept | undefined;

export function keep(k: Kept) {
	kept = k;
}

/** What the list had for this search, if it was the last one shown. */
export function keptFor(key: string): Kept | undefined {
	return kept?.key === key ? kept : undefined;
}
