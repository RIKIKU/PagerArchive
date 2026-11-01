// Prototype only (ADR 0015): the three looks and R77's menu, as the real
// build would have it. Looks differ only in the token values in app.css.
import type { IconName } from './icons.ts';

export const looks = [
	{
		id: 'ledger',
		name: 'Ledger',
		aim: [
			'Warm paper and ink with a teal accent and a humanist typeface: calm to read for long spells.',
			'Emergency rows are tinted with a red edge; other priorities are a dot and a coloured word.'
		]
	},
	{
		id: 'console',
		name: 'Console',
		aim: [
			'Ink on white or white on ink, square and dense: the most messages on screen, no accent colour.',
			'Priority is the only colour: an edge down every row and an outlined capital label.'
		]
	},
	{
		id: 'harbour',
		name: 'Harbour',
		aim: [
			'Airy, with larger type, more room and bigger targets, and a forest-green accent.',
			'Priority is a soft tinted label with a dot; rows have no edge or tint.'
		]
	}
] as const;

export type LookId = (typeof looks)[number]['id'];

export interface Entry {
	id: string;
	label: string;
	icon: IconName;
	/** R81: left out of the phone menu (still opens from a link). */
	desktopOnly?: boolean;
	count?: number;
}

export interface Group {
	id: string;
	label: string;
	icon: IconName;
	adminOnly?: boolean;
	count?: number;
	entries: Entry[];
}

export const messagesEntry: Entry = { id: 'messages', label: 'Messages', icon: 'messages' };

// R77's groups. The example user has two albums (R82, M14).
export const groups: Group[] = [
	{
		id: 'albums',
		label: 'Albums',
		icon: 'albums',
		entries: [
			{ id: 'album-1', label: 'Training: structure fires', icon: 'albums' },
			{ id: 'album-2', label: 'Unusual formats', icon: 'albums' }
		]
	},
	{
		id: 'tools',
		label: 'Tools',
		icon: 'tools',
		entries: [
			{ id: 'print', label: 'Print sheet', icon: 'print', desktopOnly: true, count: 3 },
			{ id: 'unknown', label: 'Unknown capcodes', icon: 'unknown' }
		]
	},
	{
		id: 'reports',
		label: 'Reports',
		icon: 'reports',
		entries: [{ id: 'group-report', label: 'Group report', icon: 'reports' }]
	},
	{
		id: 'account',
		label: 'Account',
		icon: 'account',
		entries: [
			{ id: 'profile', label: 'Profile', icon: 'profile' },
			{ id: 'integrations', label: 'Integrations', icon: 'integrations' },
			{ id: 'requests', label: 'My requests', icon: 'requests' },
			{ id: 'invite', label: 'Invite someone', icon: 'invite' }
		]
	},
	{
		id: 'admin',
		label: 'Administration',
		icon: 'administration',
		adminOnly: true,
		count: 2,
		entries: [
			{ id: 'users', label: 'Users', icon: 'users' },
			{ id: 'aliases', label: 'Aliases', icon: 'aliases' },
			{ id: 'templates', label: 'Templates', icon: 'templates' },
			{ id: 'clients', label: 'Clients', icon: 'clients' },
			{ id: 'retention', label: 'Retention', icon: 'retention' },
			{ id: 'waiting', label: 'Waiting requests', icon: 'waiting', count: 2 }
		]
	}
];

export interface Crumb {
	label: string;
	/** The page id to link to; the last crumb has none. */
	to?: string;
}

// Pages reached from Messages rather than the menu (R77: the map and the
// job view; R35e: the alias view), and one level below a menu page.
const below: Record<string, { label: string; parent: string }> = {
	map: { label: 'Map', parent: 'messages' },
	job: { label: 'Job EX0000014', parent: 'messages' },
	alias: { label: 'Station 1', parent: 'messages' },
	'template-fire': { label: 'Example Fire', parent: 'templates' }
};

export function findEntry(id: string): { entry: Entry; group?: Group } | undefined {
	if (id === messagesEntry.id) return { entry: messagesEntry };
	for (const group of groups) {
		const entry = group.entries.find((e) => e.id === id);
		if (entry) return { entry, group };
	}
	return undefined;
}

/** A group's own page ("group-tools"), which lists its pages (R44b's
 * Administration start page is one). */
export function findGroup(id: string): Group | undefined {
	return groups.find((g) => `group-${g.id}` === id);
}

/** R79: where a page sits in R77's tree, however it was reached. */
export function crumbs(id: string, message?: string): Crumb[] {
	if (message) return [{ label: 'Messages', to: 'messages' }, { label: message }];
	const b = below[id];
	if (b) return [...crumbs(b.parent).map((c) => ({ ...c, to: c.to ?? b.parent })), { label: b.label }];
	const group = findGroup(id);
	if (group) return [{ label: group.label }];
	const found = findEntry(id);
	if (!found) return [{ label: 'Messages' }];
	return found.group
		? [{ label: found.group.label, to: `group-${found.group.id}` }, { label: found.entry.label }]
		: [{ label: found.entry.label }];
}

export function title(id: string): string {
	return below[id]?.label ?? findGroup(id)?.label ?? findEntry(id)?.entry.label ?? 'Messages';
}
