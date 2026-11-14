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
	/** Its template's icon URL (R66). */
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
	/** Its note in the album listed, if it has one (R82f). */
	note?: string;
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
	/** The capcode asked for, or the alias (by name) and the aliases of that name. */
	capcode?: string;
	alias?: string;
	aliases?: { id: number; pattern: string }[];
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

/** R86a: the nightly backups. "off" is a stack with no backup service. */
export interface BackupStatus {
	state: 'ok' | 'waiting' | 'failed' | 'overdue' | 'off';
	last_run_at: string | null;
	last_good_at: string | null;
	last_good_night?: string;
	last_good_bytes: number;
	nights: number;
	error?: string;
	/** Back up now: what the backup service is doing. "off": it can't be asked here. */
	run: 'idle' | 'queued' | 'running' | 'off';
	requested_at: string | null;
	running_since: string | null;
}

export interface Upgrade {
	at: string;
	from: string;
	to: string;
}

/** R84h: automatic upgrades, which run on the host; the API only reads their status. */
export interface AutoUpgradeStatus {
	state: 'off' | 'ok' | 'failed' | 'stale' | 'stopped';
	/** What a run is doing now. */
	run: 'idle' | 'checking' | 'verifying' | 'backing-up' | 'upgrading' | 'health-check' | 'rolling-back';
	running_since: string | null;
	enabled_at: string | null;
	disabled_at: string | null;
	last_check_at: string | null;
	last_check_result?: string;
	last_check_note?: string;
	running_version?: string;
	latest_version?: string;
	last_upgrade: Upgrade | null;
	last_failure: (Upgrade & { step: string; reason: string; rolled_back: boolean; restored: boolean; rollback_error?: string; cleared: boolean }) | null;
	failures: number;
	rollbacks: number;
	failed_versions: string[];
	history: (Upgrade & { result: 'upgraded' | 'rolled-back' | 'rollback-failed' | '' })[];
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
	/** The days of the first and last pages (none when there are none), for the calendar's years. */
	first_page?: string;
	last_page?: string;
	/** Admins only: alias requests waiting for review (R55). */
	waiting_requests?: number;
	/** The build's version, such as "1.9.0" or "1.9.1-dev+abc1234" (12.1): the foot of the menu (R77, R81). */
	version: string;
}

export interface MessagePage {
	messages: Message[];
	next?: string;
	/** After a jump or a page of newer ones: the cursor for newer messages, if there may be any (R81b). */
	newer?: string;
}

/** One of the scrubber's marks (R81b-R81d): a span of time, newest first, and how many messages of the search it holds. */
export interface Mark {
	start: string;
	end: string;
	kind: 'hour' | 'day' | 'week' | 'month' | 'block';
	count: number;
}

export interface Counts {
	marks: Mark[];
	/** false: counting would have taken too long; the marks have no dots. */
	counted: boolean;
}

/** An album (R82, R82f): its creator's alone. */
export interface Album {
	id: number;
	name: string;
	description: string | null;
	messages: number;
	created_at: string;
	updated_at: string;
	/** Retention took its last message and it was kept for its description (R82a). */
	emptied_at: string | null;
}

/** A large event (R87c): no levels or codes (owner, 2026-11-13); its numbers are counted from the pages it still holds. */
export interface LargeEvent {
	id: number;
	start: string;
	/** Its last page's sent time. */
	end: string;
	/** How many pages it holds, all Emergency pages (R87). No appliance count (owner, 2026-11-13). */
	emergency_pages: number;
	jobs: number;
	/** The event's own view only: its job numbers, by first page. */
	job_list?: string[];
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

/** A template as admins manage it (R64). */
export interface AdminTemplate {
	id: number;
	name: string;
	agency: string;
	colour: string | null;
	agency_profile: string | null;
	icon: string | null;
	aliases: number;
	/** R64: other templates whose names differ from this one's only in letter case (from before the rule). */
	same_name_as?: string[];
}

export interface TemplateInput {
	name: string;
	agency: string;
	colour: string;
	agency_profile: string;
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

/** The looks a user can choose (R82d): Harbour, the default, and Console. */
export type Look = 'harbour' | 'console';

export interface Me {
	id: number;
	email: string;
	name: string | null;
	role: 'admin' | 'user';
	invited_by: string | null;
	/** The look they chose (R82d), saved on the account. */
	look: Look;
}

export interface Invitation {
	id: number;
	email: string;
	// link_lost: Authentik no longer has its link, though nobody used or
	// cancelled it (ADR 0021). Pending, expired and link_lost can be resent.
	status: 'pending' | 'expired' | 'link_lost' | 'used' | 'cancelled';
	created_at: string;
	resent_at: string | null;
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
	/** R87d: large events, instead of capcodes and aliases: told of each once, when it opens. */
	large_events?: boolean;
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
	large_events: boolean;
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
	/** A large event's delivery (R87d). */
	event_id?: number;
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

// A Blob (a file) is sent as it is; anything else as JSON.
async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
	const file = body instanceof Blob;
	const res = await fetch(path, {
		method,
		headers:
			body === undefined
				? { Accept: 'application/json' }
				: { Accept: 'application/json', 'Content-Type': file ? 'application/octet-stream' : 'application/json' },
		body: body === undefined ? undefined : file ? body : JSON.stringify(body)
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
	// Meta again, e.g. after a live connection reconnects (a role change ends it).
	refreshMeta: () => {
		metaPromise = undefined;
		return api.meta();
	},
	messages: (query: URLSearchParams, before?: string) => {
		const q = new URLSearchParams(query);
		if (before) q.set('before', before);
		return get<MessagePage>(`/api/v1/messages?${q}`);
	},
	/** R81b: the messages sent before a time (a jump), or the page just newer
	 * than a cursor. */
	messagesAt: (query: URLSearchParams, at: string) => {
		const q = new URLSearchParams(query);
		q.set('at', at);
		return get<MessagePage>(`/api/v1/messages?${q}`);
	},
	messagesAfter: (query: URLSearchParams, after: string) => {
		const q = new URLSearchParams(query);
		q.set('after', after);
		return get<MessagePage>(`/api/v1/messages?${q}`);
	},
	/** R81b-R81d: the scrubber's marks for a search; hour zooms into one. */
	counts: (query: URLSearchParams, hour?: string) => {
		const q = new URLSearchParams(query);
		if (hour) q.set('hour', hour);
		return get<Counts>(`/api/v1/messages/counts?${q}`);
	},
	message: (id: string | number) => get<Message>(`/api/v1/messages/${encodeURIComponent(id)}`),
	// Large events (R87c).
	largeEvents: (before?: string) => get<{ events: LargeEvent[]; next?: string }>(`/api/v1/large-events${before ? `?before=${encodeURIComponent(before)}` : ''}`),
	largeEvent: (id: string | number) => get<LargeEvent>(`/api/v1/large-events/${encodeURIComponent(id)}`),
	largeEventSettings: () => get<{ job_pages: number }>('/api/v1/admin/large-events'),
	setLargeEventSettings: (pages: number) =>
		request<{ job_pages: number }>('PUT', '/api/v1/admin/large-events', { job_pages: pages }),
	// Albums (R82-R82f).
	albums: () => get<Album[]>('/api/v1/albums'),
	album: (id: string | number) => get<Album>(`/api/v1/albums/${encodeURIComponent(id)}`),
	createAlbum: (name: string, description: string) => request<Album>('POST', '/api/v1/albums', { name, description }),
	updateAlbum: (id: number, change: { name?: string; description?: string }) => request<Album>('PATCH', `/api/v1/albums/${id}`, change),
	deleteAlbum: (id: number) => request<void>('DELETE', `/api/v1/albums/${id}`),
	addToAlbum: (album: number, message: number) => request<void>('PUT', `/api/v1/albums/${album}/messages/${message}`),
	removeFromAlbum: (album: number, message: number) => request<void>('DELETE', `/api/v1/albums/${album}/messages/${message}`),
	setNote: (album: number, message: number, note: string) =>
		request<{ note: string | null }>('PUT', `/api/v1/albums/${album}/messages/${message}/note`, { note }),
	messageAlbums: (message: number | string) => get<{ albums: number[] }>(`/api/v1/messages/${encodeURIComponent(message)}/albums`),
	/** The print sheet's messages (R67): in the order asked, and the IDs of
	 * those that no longer exist. At most MAX_SHEET IDs. */
	messagesById: (ids: number[]) =>
		get<{ messages: Message[]; missing: number[] }>(
			`/api/v1/messages/by-id?${new URLSearchParams(ids.map((id) => ['id', String(id)]))}`
		),
	job: (job: string) =>
		get<{ job_number: string; messages: Message[] }>(`/api/v1/jobs/${encodeURIComponent(job)}`),
	unaliased: () => get<UnaliasedCapcode[]>('/api/v1/capcodes/unaliased'),
	/** R33a: every alias name, agency and paging group, which the search box
	 *  suggests from (asked for once: searchValues.ts). */
	searchValues: () => get<{ alias: string[]; agency: string[]; group: string[] }>('/api/v1/search/values'),
	capcode: (capcode: string) => get<CapcodeView>(`/api/v1/capcodes/${encodeURIComponent(capcode)}`),
	alias: (id: string | number) => get<AliasView>(`/api/v1/aliases/${encodeURIComponent(id)}`),
	clients: () => get<ClientStatus[]>('/api/v1/admin/clients'),
	map: (query: URLSearchParams) => get<MapPage>(`/api/v1/map?${query}`),
	/** R38: for one capcode, or one alias (by name) and every capcode it covers. */
	report: (what: { capcode: string } | { alias: string }, from: string, to: string) =>
		get<Report>(`/api/v1/reports/capcode?${new URLSearchParams({ ...what, from, to })}`),
	retention: () => get<Retention>('/api/v1/admin/retention'),
	backup: () => get<BackupStatus>('/api/v1/admin/backup'),
	/** Back up now (R86a): the backup service starts it within a minute. */
	backUpNow: () => request<BackupStatus>('POST', '/api/v1/admin/backup'),
	/** R84h: what the host's automatic upgrades last did. */
	autoUpgrade: () => get<AutoUpgradeStatus>('/api/v1/admin/auto-upgrade'),
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
	/** R82d: saves the user's look on their account. */
	setLook: (look: Look) => request<Me>('PATCH', '/api/v1/me', { look }).then((m) => ((mePromise = Promise.resolve(m)), m)),
	invitations: () => get<Invitation[]>('/api/v1/invitations'),
	invite: (email: string) => request<Invitation>('POST', '/api/v1/invitations', { email }),
	cancelInvitation: (id: number) => request<void>('DELETE', `/api/v1/invitations/${id}`),
	/** R43d: a new link and a new 7 days; the old link stops working. */
	resendInvitation: (id: number) => request<Invitation>('POST', `/api/v1/invitations/${id}/resend`, {}),
	users: () => get<UserRow[]>('/api/v1/admin/users'),
	allInvitations: () => get<Invitation[]>('/api/v1/admin/invitations'),
	userInvitations: (id: number | string) => get<Invitation[]>(`/api/v1/admin/users/${encodeURIComponent(id)}/invitations`),
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
	// Templates and their icons (R64-R66).
	adminTemplates: () => get<AdminTemplate[]>('/api/v1/admin/templates'),
	agencyProfiles: () => get<string[]>('/api/v1/admin/agency-profiles'),
	createTemplate: (t: TemplateInput) => request<{ id: number }>('POST', '/api/v1/admin/templates', t),
	editTemplate: (id: number, t: TemplateInput) => request<void>('PATCH', `/api/v1/admin/templates/${id}`, t),
	deleteTemplate: (id: number) => request<void>('DELETE', `/api/v1/admin/templates/${id}`),
	/** Uploads an icon file; the server checks and converts it (R65). */
	setIcon: (id: number, file: File) => request<{ icon: string }>('PUT', `/api/v1/admin/templates/${id}/icon`, file),
	removeIcon: (id: number) => request<void>('DELETE', `/api/v1/admin/templates/${id}/icon`),
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
