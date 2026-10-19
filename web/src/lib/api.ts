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
	location: Location | null;
	flags?: Flags;
	receptions?: Reception[];
}

/** Where a message is placed (R24-R25). "street": no house number, or one not
 * in the dataset, so the street's centroid: approximate (section 14). */
export interface Location {
	lon: number;
	lat: number;
	source: 'address' | 'street' | 'grid';
	approximate: boolean;
}

export interface MapPage {
	messages: Message[];
	truncated: boolean;
	from: string | null;
	to: string | null;
}

export interface Report {
	capcode: string;
	from: string;
	to: string;
	categories: { category: string; count: number; codes?: { code: string | null; count: number }[] }[];
	total: number;
}

export interface NightlyResult {
	at: string;
	cutoff: string;
	deleted: number;
	redacted: number;
	ok: boolean;
	error?: string;
}

export interface Retention {
	days: number;
	default_days: number;
	min_days: number;
	max_days: number;
	next_run: string;
	cutoff: string;
	last_run: NightlyResult | null;
}

/** 409 reply to shortening retention without confirming (R46). */
export interface RetentionConfirm {
	error: string;
	code: 'confirm_cutoff';
	cutoff: string;
	message: string;
}

export interface Meta {
	role: 'admin' | 'user';
	account_url: string;
	allowed_email_domains: string[];
	time_zone: string;
	categories: string[];
	attribution: string;
	map_tiles: boolean;
	map_attribution: string;
	/** Admins only: alias requests waiting for review (R55). */
	waiting_requests?: number;
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
	template_id: number | null;
	template: string | null;
	agency: string | null;
	icon: string | null;
	colour: string | null;
	ignored: boolean;
	/** Admins only (owner, 2026-10-16). */
	history?: HistoryEntry[];
	/** The signed-in user's waiting request for this alias (R56). */
	my_request?: AliasRequest;
}

export interface CapcodeView {
	capcode: string;
	alias_id: number | null;
	messages: number;
	first_seen: string | null;
	last_seen: string | null;
	/** Admins only: entries of deleted aliases for this capcode. */
	history?: HistoryEntry[];
}

/** One change to an alias (R57). Changes map a field to [before, after]. */
export interface HistoryEntry {
	id: number;
	alias_id: number;
	alias_name: string;
	pattern: string;
	action: 'added' | 'changed' | 'deleted';
	changes: Record<string, [unknown, unknown]>;
	source: 'admin' | 'request' | 'cli' | 'import';
	made_by: string;
	requested_by: string | null;
	request_id: number | null;
	at: string;
	/** An entry of a deleted alias that had this capcode or pattern. */
	earlier: boolean;
}

export interface TemplateChoice {
	id: number;
	name: string;
	agency: string;
	icon: string | null;
	colour: string | null;
}

export type RequestKind = 'add' | 'change' | 'delete';
export type RequestStatus = 'waiting' | 'approved' | 'rejected' | 'withdrawn' | 'alias_deleted';

export interface AliasSettings {
	pattern: string;
	name: string;
	template_id: number | null;
	template: string | null;
	ignored: boolean;
}

/** A request for a new alias, a change or a deletion (R54-R56). */
export interface AliasRequest {
	id: number;
	kind: RequestKind;
	status: RequestStatus;
	alias_id: number | null;
	pattern: string;
	alias_name: string | null;
	current: AliasSettings | null;
	proposed: {
		name: string | null;
		set_template: boolean;
		template_id: number | null;
		template: string | null;
		ignored: boolean | null;
	};
	note: string | null;
	feedback: string | null;
	changed_since_requested: boolean;
	now_aliased_as: string | null;
	/** Admins only. */
	requested_by?: string;
	reviewed_by?: string;
	created_at: string;
	updated_at: string;
	closed_at: string | null;
}

export interface RequestInput {
	kind?: RequestKind;
	alias_id?: number;
	pattern?: string;
	name?: string | null;
	set_template?: boolean;
	template_id?: number | null;
	ignored?: boolean | null;
	note?: string;
}

export interface AliasInput {
	pattern: string;
	name: string;
	template_id: number | null;
	ignored: boolean;
}

export interface AdminAliasRow {
	id: number;
	pattern: string;
	name: string;
	template_id: number | null;
	template: string | null;
	ignored: boolean;
	waiting_requests: number;
}

export interface AdminAlias extends AliasView {
	requests: AliasRequest[];
	integrations: number;
}

export interface AliasDeleted {
	integrations: number;
	requests: number;
}

export interface ClientStatus {
	id: string;
	name: string;
	clock_ahead: boolean;
	last_seen: string | null;
	receiving: boolean;
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
	// R44b: their integrations, and how many are paused for failing.
	integrations: number;
	integrations_failing: number;
	// R42c: null unless trusted.
	trusted: TrustedMark | null;
}

// Invite trees (R42a-R42d, ADR 0008). Admins only.

export interface TrustedMark {
	by: string;
	at: string;
}

/** A person's checkbox in a deactivation preview. */
export interface Tick {
	can: boolean;
	why?: 'last admin' | 'you' | 'already deactivated' | 'deleted';
	start: boolean;
}

export interface TreeNode {
	id: number;
	label: string; // "name (email)", or "deleted user"
	name: string | null;
	email: string | null;
	role: 'admin' | 'user';
	status: 'active' | 'deactivated' | 'deleted';
	joined: string;
	trusted: boolean;
	invitations: number; // unused invitations that still work
	tick?: Tick; // previews only
	children: TreeNode[];
}

export interface PersonRef {
	id: number;
	label: string;
	deleted: boolean;
}

export interface InviteTree {
	inviter: PersonRef | null;
	root: TreeNode;
}

export interface DeactivationPreview extends InviteTree {
	version: string;
}

export interface TreeDeactivateResult {
	record: number;
	deactivated: number;
	spared: number;
	invitations_cancelled: number;
}

export interface DeactivationRecord {
	id: number;
	at: string;
	by: string;
	root: PersonRef;
	reason: string | null;
	deactivated: PersonRef[];
	spared: PersonRef[];
	invitations_cancelled: number;
}

// Integrations (R47-R51c, ADR 0006).

export type IntegrationKind = 'webhook' | 'mqtt' | 'discord' | 'telegram' | 'pushover' | 'ntfy';
export type Content = 'full' | 'summary';

export interface IntegrationFilter {
	categories?: string[];
	codes?: string[];
	priorities?: string[];
	text?: string;
	group?: string;
	agency?: string;
	job?: string;
	parse_failed?: boolean;
	address_unsplit?: boolean;
	location_mismatch?: boolean;
}

export interface IntegrationConfig {
	target?: string;
	auth_header?: string;
	broker?: string;
	username?: string;
	topic?: string;
	ca_cert?: string;
	ha_discovery?: boolean;
	ha_prefix?: string;
	chat_id?: string;
	server?: string;
	priority?: number | null;
}

/** Secrets are sent, never returned: an empty one keeps the saved value. */
export interface IntegrationSecrets {
	url?: string;
	auth_value?: string;
	password?: string;
	bot_token?: string;
	app_token?: string;
	user_key?: string;
	topic?: string;
	access_token?: string;
}

export interface IntegrationRequest {
	name: string;
	kind: IntegrationKind;
	content: Content | '';
	capcodes: string[];
	alias_ids: number[];
	filter: IntegrationFilter;
	config: IntegrationConfig;
	secrets: IntegrationSecrets;
}

export interface Integration {
	id: number;
	uid: string;
	name: string;
	kind: IntegrationKind;
	content: Content;
	capcodes: string[];
	// The range alias covering each capcode that one covers (R48).
	covered_by: Record<string, string>;
	aliases: { id: number; pattern: string; name: string }[];
	filter: IntegrationFilter;
	config: IntegrationConfig;
	secrets_set: Record<string, boolean>;
	secrets_error?: string;
	status: 'active' | 'paused';
	paused_by: 'failing' | 'owner' | 'admin' | null;
	paused_at: string | null;
	failed_pages: number;
	last_error: string | null;
	last_error_at: string | null;
	last_delivered_at: string | null;
	created_at: string;
	updated_at: string;
	discovery?: { ok: boolean; message: string };
}

export interface Delivery {
	message_id: number;
	capcode: string;
	sent_at: string;
	status: 'pending' | 'delivered' | 'failed' | 'too_old' | 'cancelled';
	attempts: number;
	last_result: string | null;
	last_error: string | null;
	delivered_at: string | null;
	created_at: string;
	updated_at: string;
}

export interface TestResult {
	ok: boolean;
	result: string;
	error?: string;
	body?: string;
	took_ms: number;
}

export interface AdminIntegration {
	id: number;
	name: string;
	kind: IntegrationKind;
	status: 'active' | 'paused';
	paused_by: 'failing' | 'owner' | 'admin' | null;
	last_delivered_at: string | null;
	created_at: string;
}

// What the selection field offers (R48): an alias, or a capcode with the
// range alias covering it.
export interface Suggestion {
	kind: 'alias' | 'capcode';
	alias_id?: number;
	name?: string;
	pattern?: string;
	range?: boolean;
	capcode?: string;
	covered_by?: string;
}

// One user's page in Administration (R44b).
export interface AdminUser {
	user: UserRow;
	integrations: AdminIntegration[];
	tree: InviteTree;
	deactivations: DeactivationRecord[];
}

export class ApiError extends Error {
	constructor(
		public status: number,
		message: string,
		public body?: { error?: string; code?: string; [k: string]: unknown }
	) {
		super(message);
	}
}

/**
 * Sends the browser to sign in, and back here afterwards. While the first
 * admin can still be set up, it goes to /signin instead, so a new install's
 * "Set up the first admin" button is seen (R44). Only the first call acts:
 * several requests can fail at once, and a later one's status check, cut
 * off by the first one's navigation, mustn't send the browser elsewhere.
 */
let signingIn: Promise<void> | undefined;
export function signIn() {
	if (location.pathname.startsWith('/signin')) return;
	signingIn ??= goSignIn();
}

async function goSignIn() {
	const firstAdmin = await fetch('/auth/status', { headers: { Accept: 'application/json' } })
		.then((r) => (r.ok ? (r.json() as Promise<{ first_admin_setup: boolean }>) : undefined))
		.then((s) => s?.first_admin_setup === true)
		.catch(() => false);
	if (firstAdmin) location.assign('/signin');
	else location.assign(`/auth/login?return=${encodeURIComponent(location.pathname + location.search)}`);
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
		let b;
		try {
			b = await res.json();
			if (b?.error) msg = b.error;
		} catch {
			// not JSON
		}
		throw new ApiError(res.status, msg, b);
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
	map: (query: URLSearchParams) => get<MapPage>(`/api/v1/map?${query}`),
	report: (capcode: string, from: string, to: string) =>
		get<Report>(`/api/v1/reports/capcode?${new URLSearchParams({ capcode, from, to })}`),
	retention: () => get<Retention>('/api/v1/admin/retention'),
	/** Saves a retention period; a shorter one needs the shown cut-off date
	 * confirmed, else the reply is the RetentionConfirm to show (R46). */
	setRetention: async (days: number, confirm_cutoff?: string): Promise<Retention | RetentionConfirm> => {
		try {
			return await request<Retention>('PUT', '/api/v1/admin/retention', { days, confirm_cutoff });
		} catch (e) {
			if (e instanceof ApiError && e.body?.code === 'confirm_cutoff') return e.body as unknown as RetentionConfirm;
			throw e;
		}
	},

	// Accounts (R40-R44).
	me: (fresh = false) => (fresh || !mePromise ? (mePromise = get<Me>('/api/v1/me')) : mePromise),
	deleteMe: () => request<void>('DELETE', '/api/v1/me'),
	invitations: () => get<Invitation[]>('/api/v1/invitations'),
	invite: (email: string) => request<Invitation>('POST', '/api/v1/invitations', { email }),
	cancelInvitation: (id: number) => request<void>('DELETE', `/api/v1/invitations/${id}`),
	users: () => get<UserRow[]>('/api/v1/admin/users'),
	allInvitations: () => get<Invitation[]>('/api/v1/admin/invitations'),
	deactivate: (id: number) => request<{ deactivated: number }>('POST', `/api/v1/admin/users/${id}/deactivate`, {}),
	deactivationPreview: (id: number | string) =>
		get<DeactivationPreview>(`/api/v1/admin/users/${encodeURIComponent(id)}/deactivation-preview`),
	/** Confirms a preview (R42b). A tree that changed since is refused with
	 * code "tree_changed" and the updated preview in the ApiError's body. */
	deactivateTree: (id: number, version: string, ticked: number[], reason: string) =>
		request<TreeDeactivateResult>('POST', `/api/v1/admin/users/${id}/deactivate-tree`, { version, ticked, reason }),
	setTrusted: (id: number, trusted: boolean) => request<void>('PUT', `/api/v1/admin/users/${id}/trusted`, { trusted }),
	reactivate: (id: number) => request<void>('POST', `/api/v1/admin/users/${id}/reactivate`),
	setRole: (id: number, role: 'admin' | 'user') => request<void>('PUT', `/api/v1/admin/users/${id}/role`, { role }),
	// Integrations (R47-R51c).
	selection: (q: string) => get<Suggestion[]>(`/api/v1/integrations/selection?q=${encodeURIComponent(q)}`),
	integrations: () => get<{ enabled: boolean; integrations: Integration[] }>('/api/v1/integrations'),
	createIntegration: (r: IntegrationRequest) => request<Integration>('POST', '/api/v1/integrations', r),
	updateIntegration: (id: number, r: IntegrationRequest) => request<Integration>('PUT', `/api/v1/integrations/${id}`, r),
	deleteIntegration: (id: number) => request<void>('DELETE', `/api/v1/integrations/${id}`),
	testIntegration: (id: number) => request<TestResult>('POST', `/api/v1/integrations/${id}/test`),
	pauseIntegration: (id: number) => request<void>('POST', `/api/v1/integrations/${id}/pause`),
	resumeIntegration: (id: number) => request<void>('POST', `/api/v1/integrations/${id}/resume`),
	deliveries: (id: number) => get<Delivery[]>(`/api/v1/integrations/${id}/deliveries`),
	adminUser: (id: number | string) => get<AdminUser>(`/api/v1/admin/users/${encodeURIComponent(id)}`),
	adminPauseIntegration: (id: number) => request<void>('POST', `/api/v1/admin/integrations/${id}/pause`),
	adminResumeIntegration: (id: number) => request<void>('POST', `/api/v1/admin/integrations/${id}/resume`),
	allowList: () => get<{ entries: string[] }>('/api/v1/admin/integration-hosts'),
	setAllowList: (entries: string[]) => request<{ entries: string[] }>('PUT', '/api/v1/admin/integration-hosts', { entries }),
	// Aliases in the browser (R53-R58).
	/** Fresh, not cached: the count on the Admin link (R55). */
	waitingRequests: () => get<Meta>('/api/v1/meta').then((m) => m.waiting_requests ?? 0),
	templates: () => get<TemplateChoice[]>('/api/v1/templates'),
	myRequests: () => get<AliasRequest[]>('/api/v1/alias-requests'),
	submitRequest: (r: RequestInput) => request<{ id: number }>('POST', '/api/v1/alias-requests', r),
	editRequest: (id: number, r: RequestInput) => request<void>('PUT', `/api/v1/alias-requests/${id}`, r),
	withdrawRequest: (id: number) => request<void>('POST', `/api/v1/alias-requests/${id}/withdraw`),
	adminRequests: (status: 'waiting' | 'closed') => get<AliasRequest[]>(`/api/v1/admin/alias-requests?status=${status}`),
	approveRequest: (id: number) => request<void>('POST', `/api/v1/admin/alias-requests/${id}/approve`),
	rejectRequest: (id: number, feedback: string) =>
		request<void>('POST', `/api/v1/admin/alias-requests/${id}/reject`, { feedback }),
	adminAliases: (q: string) =>
		get<{ aliases: AdminAliasRow[]; more: boolean }>(`/api/v1/admin/aliases?q=${encodeURIComponent(q)}`),
	adminAlias: (id: number | string) => get<AdminAlias>(`/api/v1/admin/aliases/${encodeURIComponent(id)}`),
	createAlias: (a: AliasInput) => request<{ id: number }>('POST', '/api/v1/admin/aliases', a),
	editAlias: (id: number, a: AliasInput) => request<void>('PUT', `/api/v1/admin/aliases/${id}`, a),
	deleteAlias: (id: number) => request<AliasDeleted>('DELETE', `/api/v1/admin/aliases/${id}`),
	authStatus: () => get<{ first_admin_setup: boolean }>('/auth/status'),
	firstAdmin: async () => {
		const r = await request<{ redirect: string }>('POST', '/auth/first-admin');
		location.assign(r.redirect);
	},
	signOut: async () => {
		const r = await request<{ redirect: string }>('POST', '/auth/logout');
		location.assign(r.redirect);
	}
};
