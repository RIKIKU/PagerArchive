// R77, R81, R77d (ADR 0051): PagerArchive's menu tree (nav.ts), with its
// counts and dots, in the shape ArchiveUx's Menu and BottomBar take it
// (archiveux/components, types.js). nav.ts stays the one source for the
// menu and the breadcrumbs (R79); this only adds what the frame shows on
// it: the albums, the waiting alias requests (R44b, R55), the print sheet's
// count (R67) and the new-large-event dot (R77c), each also said in words
// (R77a: no count or dot stands alone).
import type { Album } from './api.ts';
import { icons } from './icons.ts';
import { entryOf, messages, visible, type Entry, type Group } from './nav.ts';

/** A page in ArchiveUx's menu (its types.js Entry). */
export interface FrameEntry {
	href: string;
	label: string;
	svg?: string;
	text?: string;
	desktopOnly?: boolean;
	count?: number;
	quiet?: boolean;
	dot?: boolean;
	says?: string;
}

/** A group in ArchiveUx's menu (its types.js Group). */
export interface FrameGroup {
	id: string;
	label: string;
	svg?: string;
	text?: string;
	href: string;
	closed?: boolean;
	desktopOnly?: boolean;
	count?: number;
	dot?: boolean;
	says?: string;
	fresh?: string;
	entries: FrameEntry[];
}

/** What the frame counts, for the menu's marks. */
export interface Marks {
	admin: boolean;
	/** R55: alias requests waiting for review. */
	waiting: number;
	/** R67: messages on the print sheet. */
	sheetCount: number;
	/** R77c: a large event new to the user. */
	newEvent: boolean;
	/** R77: the user's albums, one entry each. */
	albums: Album[];
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
const waitingName = (n: number) => (n > 0 ? `, ${plural(n, 'alias request', 'alias requests')} waiting` : '');
// R77c: no dot stands alone (R77a): the names say it.
export const NEW_ENTRY = ', new in the last 4 hours';
export const NEW_GROUP = ', new large event';
export const NEW_FLYOUT = 'New large event, in the last 4 hours';

function entry(e: Entry, m: Marks): FrameEntry {
	const out: FrameEntry = { href: e.href, label: e.label, svg: icons[e.icon], text: e.text, desktopOnly: e.desktopOnly };
	if (e.href === '/sheet') return { ...out, count: m.sheetCount, quiet: true, says: `, ${plural(m.sheetCount, 'message', 'messages')}` };
	if (e.href === '/large-events' && m.newEvent) return { ...out, dot: true, says: NEW_ENTRY };
	if (e.href === '/admin/requests' && m.waiting > 0) return { ...out, count: m.waiting, says: waitingName(m.waiting) };
	return out;
}

/** The entry before the groups: Messages, at the list as it was left (R81a). */
export const menuTop = (listHref: string): FrameEntry[] => [{ href: listHref, label: messages.label, svg: icons[messages.icon], text: messages.text }];

/** The groups this person sees, with their marks. Administration starts
 * closed (menu.svelte.ts), and is left out of the phone's More (R81). */
export function menuGroups(m: Marks): FrameGroup[] {
	return visible(m.admin).map((g: Group) => {
		const dot = g.id === 'tools' && m.newEvent;
		const count = g.id === 'admin' ? m.waiting : 0;
		const says = `${waitingName(count)}${dot ? NEW_GROUP : ''}`;
		const albums: FrameEntry[] =
			g.id === 'albums'
				? m.albums.map((a) => ({ href: `/albums/${a.id}`, label: a.name, svg: icons.albums, count: a.messages, quiet: true, says: `, ${plural(a.messages, 'message', 'messages')}` }))
				: [];
		return {
			id: g.id,
			label: g.label,
			svg: icons[g.icon],
			text: g.text,
			href: g.href,
			closed: g.id === 'admin',
			desktopOnly: g.adminOnly,
			count,
			dot,
			says: says || undefined,
			fresh: dot ? NEW_FLYOUT : undefined,
			entries: [...albums, ...g.entries.map((e) => entry(e, m))]
		};
	});
}

/** The menu's current page: the entry the path is under (/admin/users/7 is
 * under Users), the Messages entry on the list, or the path itself (a
 * group's start page, an album). */
export function menuCurrent(path: string, listHref: string): string {
	if (path === '/') return listHref;
	const e = entryOf(path);
	return e ? e.entry.href : path;
}

/** R81: the bottom bar's three entries before More. */
export const bottomEntries = (listHref: string, mapHref: string): FrameEntry[] => [
	{ href: listHref, label: 'Messages', svg: icons.messages },
	{ href: mapHref, label: 'Map', svg: icons.map },
	{ href: '/albums', label: 'Albums', svg: icons.albums }
];

/** R81: More's groups: the menu's, without Albums (on the bar) and what's
 * desktop only. */
export const moreGroups = (m: Marks): FrameGroup[] => menuGroups(m).filter((g) => g.id !== 'albums');

/** The bottom bar's current page: Messages on the list and a message's
 * page, Map, Albums on every album, else the menu's. */
export function bottomCurrent(path: string, listHref: string, mapHref: string): string {
	if (path === '/' || path.startsWith('/messages/')) return listHref;
	if (path === '/map') return mapHref;
	if (path === '/albums' || path.startsWith('/albums/')) return '/albums';
	return menuCurrent(path, listHref);
}
