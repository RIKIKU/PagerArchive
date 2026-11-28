// R92g: the one Listen link a page (or a job) has to the radio beside
// PagerArchive: its dispatch channels first (R92f), then its fireground
// channels (R92a), each once, at most MAX_CHANNELS, as the link format the
// radio agreed: <RADIOARCHIVE_URL>/?fireground=508&fireground=540.
// Kept free of imports so the browser tests can check it on its own.

/** The most channels one link carries; the radio ignores any more. */
export const MAX_CHANNELS = 10;

export type Listen = {
	/** The dispatch channels it carries, in order. */
	dispatch: number[];
	/** The fireground channels it carries, in order, none a dispatch channel too. */
	fireground: number[];
	/** Every channel, dispatch first: the link's order. */
	channels: number[];
};

/** A channel as a number with no leading zeros, or undefined if it isn't one (R92a: at most 9 digits). */
function channel(v: number | string): number | undefined {
	const s = String(v).trim();
	if (!/^[0-9]+$/.test(s)) return undefined;
	const digits = s.replace(/^0+(?=[0-9])/, '');
	return digits.length > 9 ? undefined : Number(digits);
}

/**
 * The channels of one Listen link: dispatch then fireground, each in the
 * order given, each kept once (a fireground number that's also a dispatch
 * channel is the dispatch one), at most MAX_CHANNELS. Undefined for none.
 */
export function listen(dispatch: readonly (number | string)[], fireground: readonly (number | string)[]): Listen | undefined {
	const seen = new Set<number>();
	const out: Listen = { dispatch: [], fireground: [], channels: [] };
	for (const [list, into] of [
		[dispatch, out.dispatch],
		[fireground, out.fireground]
	] as const) {
		for (const v of list) {
			const n = channel(v);
			if (n === undefined || seen.has(n) || seen.size >= MAX_CHANNELS) continue;
			seen.add(n);
			into.push(n);
			out.channels.push(n);
		}
	}
	return out.channels.length ? out : undefined;
}

/** The link: <radio>/?fireground=N for each channel, in order. */
export function listenHref(radio: string, l: Listen): string {
	return `${radio}/?${l.channels.map((n) => `fireground=${n}`).join('&')}`;
}

/** "a", "a and b", "a, b and c". */
function words(ns: number[]): string {
	return ns.length < 2 ? ns.join('') : `${ns.slice(0, -1).join(', ')} and ${ns[ns.length - 1]}`;
}

/** Which channels, in words: "dispatch 508, fireground 540 and 542". */
export function listenChannels(l: Listen): string {
	return [l.dispatch.length ? `dispatch ${words(l.dispatch)}` : '', l.fireground.length ? `fireground ${words(l.fireground)}` : ''].filter(Boolean).join(', ');
}

/** The link's accessible name. */
export function listenName(l: Listen): string {
	return `Listen on RadioArchive: ${listenChannels(l)} (opens in a new tab)`;
}
