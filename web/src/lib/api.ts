// Types and calls for the PagerArchive API (internal/server).

export type Priority = 'Emergency' | 'Non-emergency' | 'Admin';

export interface Flags {
	parse_failed: boolean;
	address_unsplit: boolean;
	location_mismatch: boolean;
}

export interface Reception {
	client: string;
	received_at: string;
}

export interface Message {
	id: number;
	capcode: string;
	text: string;
	display_text: string;
	priority: Priority | null;
	heartbeat: boolean;
	sent_at: string;
	alias_id: number | null;
	alias: string | null;
	agency: string | null;
	icon: string | null;
	colour: string | null;
	ignored: boolean;
	category: string | null;
	format: string | null;
	job_number: string | null;
	event_type: string | null;
	description: string | null;
	address: string | null;
	locality: string | null;
	grid_ref: string | null;
	group: string | null;
	units: string[] | null;
	extra: Record<string, string | string[]>;
	labels: Record<string, string>;
	flags?: Flags;
	receptions?: Reception[];
}

export interface Meta {
	role: 'admin' | 'user';
	account_url: string;
	allowed_email_domains: string[];
	time_zone: string;
	categories: string[];
	attribution: string;
}

export interface MessagePage {
	messages: Message[];
	next?: string;
}

export interface UnaliasedCapcode {
	capcode: string;
	messages: number;
	first_seen: string;
	last_seen: string;
}

export interface AliasView {
	id: number;
	pattern: string;
	name: string;
	template: string | null;
	agency: string | null;
	icon: string | null;
	colour: string | null;
	ignored: boolean;
}

export interface CapcodeView {
	capcode: string;
	alias_id: number | null;
	messages: number;
	first_seen: string | null;
	last_seen: string | null;
}

export interface ClientStatus {
	id: string;
	name: string;
	revoked: boolean;
	clock_ahead: boolean;
	last_seen: string | null;
}

export interface Me {
	id: number;
	email: string;
	name: string | null;
	role: 'admin' | 'user';
	invited_by: string | null;
}

export interface Invitation {
	id: number;
	email: string;
	status: 'pending' | 'expired' | 'used' | 'cancelled';
	created_at: string;
	expires_at: string;
	used_at: string | null;
	invited_by: string;
	reinvite: boolean;
}

export interface UserRow {
	id: number;
	email: string;
	name: string | null;
	role: 'admin' | 'user';
	status: 'active' | 'deactivated';
	created_at: string;
	deactivated_at: string | null;
	invited_by_id: number | null;
	invited_by: string | null;
}

export class ApiError extends Error {
	constructor(
		public status: number,
		message: string
	) {
		super(message);
	}
}

/** Sends the browser to sign in, and back here afterwards. */
export function signIn() {
	if (location.pathname.startsWith('/signin')) return;
	location.assign(`/auth/login?return=${encodeURIComponent(location.pathname + location.search)}`);
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
	const res = await fetch(path, {
		method,
		headers: body === undefined ? { Accept: 'application/json' } : { Accept: 'application/json', 'Content-Type': 'application/json' },
		body: body === undefined ? undefined : JSON.stringify(body)
	});
	if (res.status === 401 && path.startsWith('/api/')) signIn();
	if (!res.ok) {
		let msg = `${res.status} ${res.statusText}`;
		try {
			const b = await res.json();
			if (b?.error) msg = b.error;
		} catch {
			// not JSON
		}
		throw new ApiError(res.status, msg);
	}
	if (res.status === 204) return undefined as T;
	return res.json() as Promise<T>;
}

const get = <T>(path: string) => request<T>('GET', path);

let metaPromise: Promise<Meta> | undefined;
let mePromise: Promise<Me> | undefined;

export const api = {
	meta: () => (metaPromise ??= get<Meta>('/api/v1/meta')),
	messages: (query: URLSearchParams, before?: string) => {
		const q = new URLSearchParams(query);
		if (before) q.set('before', before);
		return get<MessagePage>(`/api/v1/messages?${q}`);
	},
	message: (id: string | number) => get<Message>(`/api/v1/messages/${encodeURIComponent(id)}`),
	job: (job: string) =>
		get<{ job_number: string; messages: Message[] }>(`/api/v1/jobs/${encodeURIComponent(job)}`),
	unaliased: () => get<UnaliasedCapcode[]>('/api/v1/capcodes/unaliased'),
	capcode: (capcode: string) => get<CapcodeView>(`/api/v1/capcodes/${encodeURIComponent(capcode)}`),
	alias: (id: string | number) => get<AliasView>(`/api/v1/aliases/${encodeURIComponent(id)}`),
	clients: () => get<ClientStatus[]>('/api/v1/admin/clients'),

	// Accounts (R40-R44).
	me: (fresh = false) => (fresh || !mePromise ? (mePromise = get<Me>('/api/v1/me')) : mePromise),
	deleteMe: () => request<void>('DELETE', '/api/v1/me'),
	invitations: () => get<Invitation[]>('/api/v1/invitations'),
	invite: (email: string) => request<Invitation>('POST', '/api/v1/invitations', { email }),
	cancelInvitation: (id: number) => request<void>('DELETE', `/api/v1/invitations/${id}`),
	users: () => get<UserRow[]>('/api/v1/admin/users'),
	allInvitations: () => get<Invitation[]>('/api/v1/admin/invitations'),
	deactivate: (id: number, tree: boolean) =>
		request<{ deactivated: number }>('POST', `/api/v1/admin/users/${id}/deactivate`, { tree }),
	reactivate: (id: number) => request<void>('POST', `/api/v1/admin/users/${id}/reactivate`),
	setRole: (id: number, role: 'admin' | 'user') => request<void>('PUT', `/api/v1/admin/users/${id}/role`, { role }),
	authStatus: () => get<{ first_admin_setup: boolean }>('/auth/status'),
	signOut: async () => {
		const r = await request<{ redirect: string }>('POST', '/auth/logout');
		location.assign(r.redirect);
	}
};
