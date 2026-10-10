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
	last_seen: string;
}

export interface ClientStatus {
	id: string;
	name: string;
	revoked: boolean;
	clock_ahead: boolean;
	last_seen: string | null;
}

export class ApiError extends Error {
	constructor(
		public status: number,
		message: string
	) {
		super(message);
	}
}

async function get<T>(path: string): Promise<T> {
	const res = await fetch(path, { headers: { Accept: 'application/json' } });
	if (!res.ok) {
		let msg = `${res.status} ${res.statusText}`;
		try {
			const body = await res.json();
			if (body?.error) msg = body.error;
		} catch {
			// not JSON
		}
		throw new ApiError(res.status, msg);
	}
	return res.json() as Promise<T>;
}

let metaPromise: Promise<Meta> | undefined;

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
	clients: () => get<ClientStatus[]>('/api/v1/admin/clients')
};
