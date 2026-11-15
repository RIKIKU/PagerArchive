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
		text: 'Printing for training, capcodes with no alias, and large events',
		href: '/tools',
		entries: [
			{ href: '/sheet', label: 'Print sheet', icon: 'print', desktopOnly: true, text: 'Pick messages and print their text for training.' },
			{ href: '/capcodes', label: 'Unknown capcodes', icon: 'unknown', text: 'Capcodes with no alias, how many pages each had and when.' },
			{ href: '/large-events', label: 'Large events', icon: 'largeevents', text: 'Times something big happened on the network, so you know where to look, grouped by 12 hours.' }
		]
	},
	{
		id: 'reports',
		label: 'Reports',
		icon: 'reports',
		text: 'Counts of past pages and jobs',
		href: '/reports',
		entries: [
			{ href: '/reports/capcode', label: 'Capcode report', icon: 'reports', text: "One capcode's Emergency messages per event category, for a date range." },
			{ href: '/reports/busiest', label: 'Busiest capcodes', icon: 'busiest', text: 'The aliases and capcodes with the most jobs, and how that changed from the period before.' },
			{ href: '/reports/localities', label: 'Busiest localities', icon: 'localities', text: 'Jobs by locality, in a table and on the map.' },
			{ href: '/reports/busy-times', label: "When it's busy", icon: 'busytimes', text: 'Jobs by hour of the day and day of the week.' },
			{ href: '/reports/event-types', label: 'Event type mix', icon: 'eventtypes', text: "Each event type's share of the jobs, and how it changes month by month." },
			{ href: '/reports/job-size', label: 'Job size', icon: 'jobsize', text: 'Pages, capcodes and appliances per job: typical and spread, by event type.' },
			{ href: '/reports/long-jobs', label: 'Long-running jobs', icon: 'longjobs', text: 'The jobs with the longest time from first to last page.' },
			{ href: '/reports/appliances', label: 'Appliance activity', icon: 'appliances', text: 'How often each appliance is named in jobs.' },
			{ href: '/reports/large-events', label: 'Large events over time', icon: 'eventsovertime', text: 'Large events per week or month, how long they last and how many jobs they hold.' }
		]
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
			{ href: '/admin/upgrades', label: 'Upgrades', icon: 'upgrades', desktopOnly: true, text: 'Whether automatic upgrades are on, the last check and upgrade, and any that failed.' },
			{ href: '/admin/integration-hosts', label: 'Integration addresses', icon: 'hosts', desktopOnly: true, text: 'Private hosts and ranges that integrations may reach.' },
			{ href: '/admin/large-events', label: 'Large event settings', icon: 'largeevents', desktopOnly: true, text: 'How many Emergency pages on one job open a large event.' }
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
		case '/large-events/[id]':
			return under([group('tools'), { label: 'Large events', href: '/large-events' }], title || 'Large event');
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
