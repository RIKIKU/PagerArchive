// R33a: one search box. What's typed is a list of words: a word that starts
// with a known key ("alias:") is a filter, and everything else is text, so a
// page's own colons (ADDR:, 14:05) still search as text. A value with spaces
// is quoted (alias:"Station 1"). A quoted alias, agency, group or category
// matches that value exactly, as picking a suggestion does; an unquoted one
// matches every value containing it (alias:hill). The same key more than
// once means any of them; different keys must all match.
//
// The text is terms (owner, 2026-11-02): each word, or each phrase in
// quotes, must be in the page; an or: term is an alternative to all the
// others together. The API splits the text into terms (internal/search), so
// the text goes to it as typed, quotes and or: included.
//
// The search is kept in the page's address, one parameter per filter, so a
// search can be bookmarked and shared, and the addresses from before the
// single box (?job=…, ?capcode=…) still work. Those parameters are turned
// into the API's here.

export const keys = ['alias', 'agency', 'capcode', 'group', 'priority', 'category', 'job', 'is', 'or', 'flag'] as const;
export type Key = (typeof keys)[number];

/** The keys this person can use: flag: is for admins only. */
export const keysFor = (admin: boolean): Key[] => keys.filter((k) => admin || k !== 'flag');

/** The help control's examples, one per key (R33a). */
export const examples: Record<Key, { example: string; what: string }> = {
	alias: { example: 'alias:"Station 1"', what: 'Pages to an alias. alias:stat finds every alias containing "stat".' },
	agency: { example: 'agency:fire', what: "Pages to an agency's aliases." },
	capcode: { example: 'capcode:100001', what: 'Pages to one capcode, exactly.' },
	group: { example: 'group:STN1', what: 'Pages to a paging group.' },
	priority: { example: 'priority:emergency', what: 'Emergency, non-emergency, admin or none.' },
	category: { example: 'category:"Structure fire"', what: 'Pages of one event category.' },
	job: { example: 'job:EX0000001', what: 'Pages of one job, exactly.' },
	is: { example: 'is:unaliased', what: 'is:unaliased: capcodes with no alias. is:ignored: include ignored aliases.' },
	or: { example: 'or:"grass fire"', what: 'Or pages with this instead of all the other words. To find "or:" itself, put it in quotes.' },
	flag: { example: 'flag:parse_failed', what: 'parse_failed, address_unsplit or location_mismatch.' }
};

/** A few words on each key, beside it in the suggestions (M21). */
export const hints: Record<Key, string> = {
	alias: 'an alias, by name',
	agency: "an agency's aliases",
	capcode: 'one capcode, exactly',
	group: 'a paging group',
	priority: 'emergency, non-emergency, admin or none',
	category: 'an event category',
	job: 'one job, exactly',
	is: 'unaliased or ignored',
	or: 'or this, instead of the other words',
	flag: 'admins only: a parsing flag'
};

export const priorities = ['Emergency', 'Non-emergency', 'Admin', 'none'] as const;
export const isValues = ['unaliased', 'ignored'] as const;
export const flagValues = ['parse_failed', 'address_unsplit', 'location_mismatch'] as const;

export interface Word {
	key?: Key;
	value: string;
	quoted: boolean;
	start: number;
	end: number;
}

/** The words in a box, each with where it is (for suggestions). */
export function words(text: string, admin: boolean): Word[] {
	const allowed = keysFor(admin);
	const out: Word[] = [];
	let i = 0;
	while (i < text.length) {
		if (/\s/.test(text[i])) {
			i++;
			continue;
		}
		const start = i;
		let key: Key | undefined;
		const m = /^([a-z]+):/i.exec(text.slice(i));
		if (m && (allowed as string[]).includes(m[1].toLowerCase())) {
			key = m[1].toLowerCase() as Key;
			i += m[0].length;
		}
		let value = '';
		let quoted = false;
		if (text[i] === '"') {
			quoted = true;
			const close = text.indexOf('"', i + 1);
			const end = close === -1 ? text.length : close;
			value = text.slice(i + 1, end);
			i = close === -1 ? text.length : close + 1;
		} else {
			while (i < text.length && !/\s/.test(text[i])) i++;
			value = text.slice(key ? start + key.length + 1 : start, i);
		}
		out.push({ key, value, quoted, start, end: i });
	}
	return out;
}

/** A value as the box shows it: quoted when it must be, or when exact. */
export function show(value: string, exact: boolean): string {
	return exact || /[\s"]/.test(value) || value === '' ? `"${value.replaceAll('"', '')}"` : value;
}

// Address parameter for each key, and back. is: and flag: values become the
// API's own true/false parameters.
const boolParams: Record<string, [Key, string]> = {
	unaliased: ['is', 'unaliased'],
	ignored: ['is', 'ignored'],
	parse_failed: ['flag', 'parse_failed'],
	address_unsplit: ['flag', 'address_unsplit'],
	location_mismatch: ['flag', 'location_mismatch']
};
const exactKeys: Key[] = ['alias', 'agency', 'group', 'category'];

/** The parameters of a search in the address, without unrelated ones. */
export const addressKeys = ['q', 'alias', 'agency', 'capcode', 'group', 'priority', 'category', 'job', 'from', 'to', ...Object.keys(boolParams)];

export function criteria(url: { searchParams: Iterable<[string, string]> }): URLSearchParams {
	const out = new URLSearchParams();
	for (const [k, v] of url.searchParams) if (addressKeys.includes(k) && v) out.append(k, v);
	return out;
}

export const isFiltered = (q: URLSearchParams) => [...q.keys()].some((k) => k !== 'from' && k !== 'to');

/** The box's text for a search in the address. */
export function boxText(q: URLSearchParams): string {
	const parts: string[] = [];
	for (const key of ['alias', 'agency', 'capcode', 'group', 'priority', 'category', 'job'] as const) {
		for (const v of q.getAll(key)) {
			const exact = v.length > 1 && v.startsWith('"') && v.endsWith('"');
			const value = exact ? v.slice(1, -1) : v;
			parts.push(`${key}:${exactKeys.includes(key) ? show(value, exact) : show(value, false)}`);
		}
	}
	for (const [param, [key, value]] of Object.entries(boolParams)) if (q.get(param) === 'true') parts.push(`${key}:${value}`);
	const text = q.get('q');
	if (text) parts.push(text);
	return parts.join(' ');
}

/** Why a box can't be searched ('' when it can): an unknown value. */
export interface Parsed {
	params: URLSearchParams;
	problem: string;
}

/** The address parameters for what's in the box (from and to aside). */
export function parseBox(text: string, admin: boolean): Parsed {
	const params = new URLSearchParams();
	const free: string[] = [];
	let problem = '';
	// The text terms, as typed: a phrase keeps its quotes and or: its key, so
	// the API reads the same terms (R33a).
	const term = (v: string, quoted: boolean) => (quoted ? `"${v}"` : v);
	for (const w of words(text, admin)) {
		if (!w.key) {
			if (w.value) free.push(term(w.value, w.quoted));
			continue;
		}
		const v = w.value.trim();
		if (!v) continue;
		switch (w.key) {
			case 'or':
				free.push(`or:${term(v, w.quoted)}`);
				break;
			case 'priority': {
				const p = priorities.find((x) => x.toLowerCase() === v.toLowerCase().replace(/^nonemergency$/, 'non-emergency'));
				if (p) params.append('priority', p);
				else problem ||= `There's no priority "${v}". Use emergency, non-emergency, admin or none.`;
				break;
			}
			case 'is':
				if ((isValues as readonly string[]).includes(v.toLowerCase())) params.set(v.toLowerCase(), 'true');
				else problem ||= `There's no is:${v}. Use is:unaliased or is:ignored.`;
				break;
			case 'flag':
				if ((flagValues as readonly string[]).includes(v.toLowerCase())) params.set(v.toLowerCase(), 'true');
				else problem ||= `There's no flag "${v}". Use parse_failed, address_unsplit or location_mismatch.`;
				break;
			case 'capcode':
			case 'job':
				params.append(w.key, v);
				break;
			default:
				params.append(w.key, w.quoted ? `"${v}"` : v);
		}
	}
	if (free.length) params.set('q', free.join(' '));
	return { params, problem };
}

const likeEscape = (s: string) => s.replace(/[\\%_]/g, (c) => `\\${c}`);

/**
 * The API's parameters for a search in the address: an exact alias or agency
 * becomes a pattern with nothing to fill in; a partial group a pattern with
 * the value inside; a partial category every category containing it (the
 * network's categories are in meta). problem says why it can't be searched.
 */
export function apiParams(q: URLSearchParams, categories: string[]): { params: URLSearchParams; problem: string } {
	const out = new URLSearchParams();
	let problem = '';
	for (const [k, raw] of q) {
		const exact = raw.length > 1 && raw.startsWith('"') && raw.endsWith('"');
		const v = exact ? raw.slice(1, -1) : raw;
		switch (k) {
			case 'alias':
			case 'agency':
				if (exact) out.append(`${k}_like`, likeEscape(v));
				else out.append(k, v);
				break;
			case 'group':
				if (exact) out.append('group', v);
				else out.append('group_like', `%${likeEscape(v)}%`);
				break;
			case 'category': {
				const all = [...categories, 'Other'];
				const found = exact ? all.filter((c) => c.toLowerCase() === v.toLowerCase()) : all.filter((c) => c.toLowerCase().includes(v.toLowerCase()));
				if (found.length) for (const c of found) out.append('category', c);
				else problem ||= `No event category matches "${v}".`;
				break;
			}
			default:
				out.append(k, raw);
		}
	}
	return { params: out, problem };
}

// Suggestions (R33a, M21). Clicking into the box lists every key. A key
// being typed suggests its values; a word without a key suggests searching
// it as text (what Enter does), the keys starting with it, and then the
// alias, agency, group and category values containing it, with their key.
// capcode:, job: and or: have no values, and plain words never suggest
// capcodes or job numbers. The values come from SearchValues, asked for once
// (searchValues.ts), so typing makes no requests.

/** What's suggested from: the API's alias:, agency: and group: values, and
 *  meta's categories. */
export interface Values {
	alias: string[];
	agency: string[];
	group: string[];
	category: string[];
}

export type Suggestion =
	| { kind: 'text'; id: string; text: string }
	| { kind: 'key'; id: string; key: Key; hint: string; section?: string }
	| { kind: 'value'; id: string; key: Key; value: string; insert: string; section?: string };

export interface Suggestions {
	/** The word the suggestions replace; none for the keys at the cursor. */
	target?: Word;
	options: Suggestion[];
	/** What the options are, for the count that's announced. */
	what: 'filter key' | 'suggestion';
}

/** Each section's heading, for a word with no key. */
export const sections: Partial<Record<Key, string>> = { alias: 'Aliases', agency: 'Agencies', group: 'Groups', category: 'Categories' };

/** At most this many values after a key, and for a word with no key. */
const afterKey = 20;
const plainRoom = 8;

/** The values containing typed, ignoring case: those starting with it first,
 *  then those with a word starting with it, then the rest, each in order. */
export function matching(list: readonly string[], typed: string, limit = Infinity): string[] {
	const t = typed.toLowerCase();
	const ranked: string[][] = [[], [], []];
	for (const v of list) {
		const l = v.toLowerCase();
		const at = l.indexOf(t);
		if (at < 0) continue;
		ranked[at === 0 ? 0 : /[\s\-/(]/.test(l[at - 1]) ? 1 : 2].push(v);
	}
	return ranked.flat().slice(0, limit);
}

/** The values a key suggests, all of them (none for capcode:, job:, or:). */
export function valuesOf(key: Key, values: Values, admin: boolean): readonly string[] {
	switch (key) {
		case 'alias':
		case 'agency':
		case 'group':
			return values[key];
		case 'category':
			return [...values.category, 'Other'];
		case 'priority':
			return priorities.map((p) => p.toLowerCase());
		case 'is':
			return isValues;
		case 'flag':
			return admin ? flagValues : [];
		default:
			return [];
	}
}

/** What picking a value puts in the box: exact for a named value. */
export const filterText = (key: Key, value: string) => `${key}:${show(value, exactKeys.includes(key))}`;

const keyOption = (key: Key, section?: string): Suggestion => ({ kind: 'key', id: `key-${key}`, key, hint: hints[key], section });

/** Every key, for the cursor where no word is being typed. */
export function keySuggestions(admin: boolean): Suggestions {
	return { options: keysFor(admin).map((k) => keyOption(k)), what: 'filter key' };
}

/** The suggestions for the word at the caret. */
export function suggestions(text: string, caret: number, admin: boolean, values: Values): Suggestions {
	const target = words(text, admin).find((w) => w.start <= caret && caret <= w.end);
	if (!target || (!target.key && !target.value)) return keySuggestions(admin);
	const value = (key: Key, v: string, section?: string): Suggestion => ({ kind: 'value', id: `${key}-${v}`, key, value: v, insert: filterText(key, v), section });
	if (target.key) {
		const key = target.key;
		const list = valuesOf(key, values, admin);
		return { target, options: matching(list, target.value, afterKey).map((v) => value(key, v)), what: 'suggestion' };
	}
	const typed = target.value;
	const options: Suggestion[] = [{ kind: 'text', id: 'text', text: target.quoted ? `"${typed}"` : typed }];
	if (!target.quoted) {
		const t = typed.toLowerCase();
		for (const k of keysFor(admin)) if (k.startsWith(t) && k !== t) options.push(keyOption(k, 'Filter keys'));
		let room = plainRoom;
		for (const key of ['alias', 'agency', 'group', 'category'] as const) {
			for (const v of matching(valuesOf(key, values, admin), typed, room)) options.push(value(key, v, sections[key]));
			room = plainRoom - options.filter((o) => o.kind === 'value').length;
		}
	}
	return { target, options, what: 'suggestion' };
}
