// R77: the side menu's tree, and R79: each page's place in it, for the
// breadcrumbs. The tree is the one source for both, so a page's breadcrumbs
// are the same however it was reached. The Albums group's pages are the
// person's own albums (R82), added by the menu from albums.svelte.ts.
import type { IconName } from './icons.ts';

export interface Entry {
	href: string;
	label: string;
	icon: IconName;
	/** What it's for, on its group's start page. */
	text: string;
	/** R81: left out of the phone menu (still opens from a link). */
	desktopOnly?: boolean;
}

export interface Group {
	id: 'albums' | 'tools' | 'reports' | 'account' | 'admin';
	label: string;
	icon: IconName;
	/** What's in it, in a few words: the shrunk menu's tooltip for it. */
	text: string;
	/** The group's start page, listing its pages (ADR 0015, decision 4). */
	href: string;
	adminOnly?: boolean;
	entries: Entry[];
}

export const messages: Entry = { href: '/', label: 'Messages', icon: 'messages', text: 'Every page, newest first, as it arrives' };

export const groups: Group[] = [
	{
		id: 'albums',
		label: 'Albums',
		icon: 'albums',
		text: 'Messages you keep, with your own notes',
		href: '/albums',
		// One per album, from the API (R77): see Menu.svelte.
		entries: []
	},
	{
		id: 'tools',
		label: 'Tools',
		icon: 'tools',
		text: 'Printing for training, and capcodes with no alias',
		href: '/tools',
		entries: [
			{ href: '/sheet', label: 'Print sheet', icon: 'print', desktopOnly: true, text: 'Pick messages and print their text for training.' },
			{ href: '/capcodes', label: 'Unknown capcodes', icon: 'unknown', text: 'Capcodes with no alias, how many pages each had and when.' }
		]
	},
	{
		id: 'reports',
		label: 'Reports',
		icon: 'reports',
		text: 'Counts of past pages',
		href: '/reports',
		entries: [{ href: '/reports/capcode', label: 'Capcode report', icon: 'reports', text: "One capcode's Emergency messages per event category, for a date range." }]
	},
	{
		id: 'account',
		label: 'Account',
		icon: 'account',
		text: 'Your profile, integrations, requests and invitations',
		href: '/account',
		entries: [
			{ href: '/account/profile', label: 'Profile', icon: 'profile', text: 'Who you are, who invited you, and deleting your account.' },
			{ href: '/integrations', label: 'Integrations', icon: 'integrations', text: 'Send pages on to your own systems.' },
			{ href: '/requests', label: 'My requests', icon: 'requests', text: 'Aliases you asked for or asked to change, and what the admins said.' },
			{ href: '/account/invite', label: 'Invite someone', icon: 'invite', text: 'Send someone an invitation, and see the ones you sent.' }
		]
	},
	{
		id: 'admin',
		label: 'Administration',
		icon: 'administration',
		text: 'Users, aliases, templates and the server',
		href: '/admin',
		adminOnly: true,
		entries: [
			{ href: '/admin/users', label: 'Users', icon: 'users', desktopOnly: true, text: 'Users and their integrations, roles, deactivation and unused invitations.' },
			{ href: '/admin/aliases', label: 'Aliases', icon: 'aliases', desktopOnly: true, text: 'Add, edit and delete aliases, see their history, and export them as a CSV.' },
			{ href: '/admin/templates', label: 'Templates', icon: 'templates', desktopOnly: true, text: 'Add, edit and delete templates: agency, icon, colour and agency profile.' },
			{ href: '/admin/requests', label: 'Alias requests', icon: 'waiting', desktopOnly: true, text: 'Requests from users for new aliases, changes and deletions, to approve or reject.' },
			{ href: '/admin/clients', label: 'Receiving clients', icon: 'clients', desktopOnly: true, text: 'Which receivers are sending pages, and which have gone quiet.' },
			{ href: '/admin/retention', label: 'Retention', icon: 'retention', desktopOnly: true, text: 'How long messages are kept, and the nightly clean-up.' },
			{ href: '/admin/backups', label: 'Backups', icon: 'backups', desktopOnly: true, text: 'Whether the nightly backups are working, and the last good one.' },
			{ href: '/admin/integration-hosts', label: 'Integration addresses', icon: 'hosts', desktopOnly: true, text: 'Private hosts and ranges that integrations may reach.' }
		]
	}
];

/** The groups this person sees. */
export const visible = (admin: boolean) => groups.filter((g) => admin || !g.adminOnly);

/** The menu entry a path belongs to: its own, or the one above it
 * (/admin/users/7 is under Users). */
export function entryOf(path: string): { entry: Entry; group?: Group } | undefined {
	if (path === '/') return { entry: messages };
	let best: { entry: Entry; group: Group } | undefined;
	for (const group of groups) {
		for (const entry of group.entries) {
			if (entry.href.includes('#')) continue;
			if (path === entry.href || path.startsWith(entry.href + '/')) {
				if (!best || entry.href.length > best.entry.href.length) best = { entry, group };
			}
		}
	}
	return best;
}

export const groupAt = (path: string) => groups.find((g) => g.href === path);

export interface Crumb {
	label: string;
	href?: string;
}

/**
 * R79: where a page sits in R77's tree, from its route, whatever way it was
 * reached. title is the page's own name when it comes from data (an alias's
 * name), once loaded. The last crumb is the page itself and has no link.
 */
export function crumbs(route: string, params: Record<string, string>, title: string): Crumb[] {
	const under = (parents: Crumb[], label: string): Crumb[] => [...parents, { label }];
	const top: Crumb = { label: messages.label, href: messages.href };
	const group = (id: Group['id']): Crumb => {
		const g = groups.find((x) => x.id === id)!;
		return { label: g.label, href: g.href };
	};
	const admin = group('admin');
	const users = { label: 'Users', href: '/admin/users' };
	switch (route) {
		case '/(list)':
			return [{ label: messages.label }];
		case '/(list)/messages/[id]':
			return under([top], title || 'Message');
		case '/map':
			return under([top], 'Map');
		case '/jobs/[job]':
			return under([top], `Job ${params.job}`);
		case '/aliases/[id]':
			return under([top], title || 'Alias');
		case '/capcodes/[capcode]':
			return under([top], `Capcode ${params.capcode}`);
		case '/albums/[id]':
			return under([group('albums')], title || 'Album');
		case '/admin/users/[id]':
			return under([admin, users], title || 'User');
		case '/admin/users/[id]/deactivate-tree':
			return under([admin, users, { label: title || 'User', href: `/admin/users/${params.id}` }], 'Deactivate with invite tree');
		case '/admin/aliases/[id]':
			return under([admin, { label: 'Aliases', href: '/admin/aliases' }], title || 'Alias');
		case '/admin/templates/[id]':
			return under([admin, { label: 'Templates', href: '/admin/templates' }], title || 'Template');
	}
	const g = groups.find((x) => x.href === route);
	if (g) return [{ label: g.label }];
	const found = entryOf(route);
	if (found?.group) return under([group(found.group.id)], found.entry.label);
	return [{ label: title || messages.label }];
}
