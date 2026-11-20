<script lang="ts">
	import { untrack } from 'svelte';
	// Creates or edits one integration (R47-R51c): selection, filter,
	// destination and content. Secrets are never shown: a saved one stays
	// unless a new value is typed (R51).
	import {
		api,
		type Content,
		type Integration,
		type IntegrationKind,
		type IntegrationRequest,
		type IntegrationSecrets,
		type LevelChoice,
		type LevelFilter,
		type Meta
	} from './api.ts';
	import { kindLabels, presets } from './integrations.ts';
	import SelectionField from './SelectionField.svelte';
	import FieldError from './FieldError.svelte';
	import FormSummary from './FormSummary.svelte';
	import { FormCheck } from './formCheck.svelte.ts';

	let {
		existing,
		meta,
		onsaved,
		oncancel
	}: {
		existing?: Integration;
		meta: Meta;
		onsaved: (i: Integration) => void;
		oncancel: () => void;
	} = $props();

	// A form edits what it was opened with: each edit mounts a new one.
	const init = untrack(() => existing);
	const id = `int-${init?.id ?? 'new'}`;
	let name = $state(init?.name ?? '');
	let kind: IntegrationKind = $state(init?.kind ?? 'webhook');
	let content: Content = $state(init?.content ?? 'full');
	let capcodes: string[] = $state([...(init?.capcodes ?? [])]);
	let chosenAliases = $state([...(init?.aliases ?? [])]);
	// R87d: large events, instead of capcodes and aliases.
	let selects: 'pages' | 'events' = $state(init?.large_events ? 'events' : 'pages');
	let coveredBy: Record<string, string> = $state({ ...(init?.covered_by ?? {}) });
	let categories: string[] = $state([...(init?.filter.categories ?? [])]);
	let codes = $state((init?.filter.codes ?? []).join(', '));
	let priorities: string[] = $state([...(init?.filter.priorities ?? [])]);
	let text = $state(init?.filter.text ?? '');
	let callSign = $state(init?.filter.call_sign ?? '');
	let agency = $state(init?.filter.agency ?? '');
	let job = $state(init?.filter.job ?? '');
	// R49a: "<field> at or above <level>" for one agency profile. The levels
	// are shown here only: never on pages, jobs or notifications (R87b).
	const savedLevel = init?.filter.level ?? null;
	const levelKeyOf = (profile: string, field: string) => `${profile}/${field}`;
	let levelChoices: LevelChoice[] = $state([]);
	let levelsLoaded = $state(false);
	let levelKey = $state(savedLevel ? levelKeyOf(savedLevel.profile, savedLevel.field) : '');
	let atLeast = $state(savedLevel?.at_least ?? '');
	const levelChoice = $derived(levelChoices.find((c) => levelKeyOf(c.profile, c.field) === levelKey));
	// A saved level whose profile or field isn't loaded now: kept until changed.
	const levelGone = $derived(
		levelsLoaded && !!savedLevel && !levelChoices.some((c) => levelKeyOf(c.profile, c.field) === levelKeyOf(savedLevel.profile, savedLevel.field))
	);
	api.integrationLevels().then(
		(l) => {
			levelChoices = l;
			levelsLoaded = true;
		},
		() => {}
	);

	function levelFieldChanged() {
		// A new field starts at its lowest level, unless the level is one of its own.
		if (levelChoice && !levelChoice.levels.some((l) => l.value === atLeast)) atLeast = levelChoice.levels[0]?.value ?? '';
	}

	function levelFilter(): LevelFilter | null {
		if (levelChoice && atLeast) return { profile: levelChoice.profile, field: levelChoice.field, at_least: atLeast };
		if (savedLevel && levelKey === levelKeyOf(savedLevel.profile, savedLevel.field)) return savedLevel;
		return null;
	}
	let parseFailed = $state(init?.filter.parse_failed ?? false);
	let addressUnsplit = $state(init?.filter.address_unsplit ?? false);
	let locationMismatch = $state(init?.filter.location_mismatch ?? false);
	let config = $state({ ...(init?.config ?? {}) });
	let priority = $state(init?.config.priority == null ? 'page' : String(init.config.priority));
	let secrets: IntegrationSecrets = $state({});
	let saving = $state(false);
	let error = $state('');

	const admin = $derived(meta.role === 'admin');

	function saved(field: string): string {
		return existing?.secrets_set[field] ? 'Saved. Leave empty to keep it.' : '';
	}

	// R82e: the word "required" in each required field's label. A secret is
	// required only while none is saved, and the auth header's value only
	// with a header name (and none saved). The form checks itself on save.
	const fc = new FormCheck();
	const secretNeeded = (field: string) => !existing?.secrets_set[field];
	const secretMarker = (field: string) => (!secretNeeded(field) ? '' : existing ? 'required: none saved yet' : 'required');
	const headerValueNeeded = $derived(!!config.auth_header?.trim() && secretNeeded('auth_value'));
	const f = (name: string) => `${id}-${name}`;

	function kindChanged() {
		// Presets go to third parties: Summary unless chosen otherwise (R50a).
		content = presets.includes(kind) ? 'summary' : 'full';
		priority = 'page';
	}

	function toggle(list: string[], v: string, on: boolean): string[] {
		return on ? [...list.filter((x) => x !== v), v] : list.filter((x) => x !== v);
	}

	const words = (s: string) => s.split(/[\s,]+/).filter(Boolean);

	async function submit(ev: SubmitEvent) {
		ev.preventDefault();
		error = '';
		const noPages = selects === 'pages' && capcodes.length === 0 && chosenAliases.length === 0;
		if (!fc.check(ev.currentTarget as HTMLFormElement, { [f('pages')]: noPages && 'Nothing chosen yet: choose at least one capcode or alias.' })) return;
		saving = true;
		const req: IntegrationRequest = {
			name,
			kind,
			content,
			capcodes,
			alias_ids: chosenAliases.map((a) => a.id),
			filter: {
				categories,
				codes: words(codes),
				priorities,
				text,
				call_sign: callSign,
				agency,
				job,
				level: levelFilter(),
				parse_failed: admin && parseFailed,
				address_unsplit: admin && addressUnsplit,
				location_mismatch: admin && locationMismatch
			},
			config: { ...config, target: undefined, priority: priority === 'page' ? null : Number(priority) },
			secrets: Object.fromEntries(Object.entries(secrets).filter(([, v]) => v)) as IntegrationSecrets
		};
		if (selects === 'events') {
			// A summary of the event, never page text: no filter, no content to choose.
			Object.assign(req, { capcodes: [], alias_ids: [], large_events: true, content: 'summary', filter: {} });
		}
		try {
			const out = existing ? await api.updateIntegration(existing.id, req) : await api.createIntegration(req);
			onsaved(out);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			saving = false;
		}
	}
</script>

<form class="integration-form" novalidate onsubmit={submit} oninput={fc.cleared} onchange={fc.cleared} aria-labelledby="{id}-heading">
	<h2 id="{id}-heading">{existing ? `Edit “${existing.name}”` : 'New integration'}</h2>
	<FormSummary check={fc} />

	<div class="field">
		<label for={f('name')}>Name <span class="required">required</span></label>
		<input id={f('name')} bind:value={name} required maxlength="80" autocomplete="off" aria-invalid={fc.invalid(f('name'))} aria-describedby={fc.describedBy(f('name'))} />
		<FieldError check={fc} id={f('name')} />
	</div>

	<div class="field">
		<label for="{id}-kind">Send to</label>
		<select id="{id}-kind" bind:value={kind} onchange={kindChanged} disabled={!!existing}>
			{#each Object.entries(kindLabels) as [k, label] (k)}
				<option value={k}>{label}</option>
			{/each}
		</select>
	</div>

	<fieldset>
		<legend>What it sends</legend>
		<div class="stacked">
			<label class="check"><input type="radio" bind:group={selects} value="pages" /> Pages from capcodes and aliases</label>
			<label class="check"><input type="radio" bind:group={selects} value="events" /> Large events: a summary when one opens</label>
		</div>
	</fieldset>

	{#if selects === 'events'}
		<p class="help" data-testid="large-event-help">
			It sends once for each large event, when the event opens: when it started, the page that opened it, its job numbers so far, how
			many Emergency pages it holds, and a link to the event. Never page text. Nothing more is sent as the event grows.
		</p>
	{:else}
		<fieldset id={f('pages')} aria-describedby={fc.describedBy(f('pages'), f('pages-hint'))}>
			<legend>Which pages <span class="required">required</span></legend>
			<p class="field-hint" id={f('pages-hint')}>Choose at least one capcode or alias.</p>
			<SelectionField id="{id}-selection" bind:capcodes bind:aliases={chosenAliases} bind:coveredBy />
			<FieldError check={fc} id={f('pages')} />
		</fieldset>

		<fieldset>
			<legend>Only these</legend>
			<p class="help">Leave everything empty to send every page from the capcodes and aliases above.</p>
			<fieldset class="inner">
				<legend>Event categories</legend>
				<div class="checks">
					{#each meta.categories as cat (cat)}
						<label class="check"
							><input
								type="checkbox"
								checked={categories.includes(cat)}
								onchange={(e) => (categories = toggle(categories, cat, e.currentTarget.checked))}
							/>
							{cat}</label
						>
					{/each}
				</div>
			</fieldset>
			<div class="field">
				<label for="{id}-codes">Event-type codes</label>
				<input id="{id}-codes" bind:value={codes} placeholder="HSF1, GRS3" autocomplete="off" autocapitalize="characters" spellcheck="false" />
			</div>
			<fieldset class="inner">
				<legend>Priority</legend>
				<div class="checks">
					{#each [['Emergency', 'Emergency'], ['Non-emergency', 'Non-emergency'], ['Admin', 'Admin'], ['none', 'No priority']] as [v, label] (v)}
						<label class="check"
							><input
								type="checkbox"
								checked={priorities.includes(v)}
								onchange={(e) => (priorities = toggle(priorities, v, e.currentTarget.checked))}
							/>
							{label}</label
						>
					{/each}
				</div>
			</fieldset>
			<div class="grid">
				<div class="field">
					<label for="{id}-text">Text contains</label>
					<input id="{id}-text" bind:value={text} autocomplete="off" />
				</div>
				<div class="field">
					<label for="{id}-callsign">Call sign</label>
					<input id="{id}-callsign" bind:value={callSign} autocomplete="off" autocapitalize="characters" spellcheck="false" />
				</div>
				<div class="field">
					<label for="{id}-agency">Agency</label>
					<input id="{id}-agency" bind:value={agency} autocomplete="off" />
				</div>
				<div class="field">
					<label for="{id}-job">Job number</label>
					<input id="{id}-job" bind:value={job} autocomplete="off" autocapitalize="characters" spellcheck="false" />
				</div>
			</div>
			{#if levelChoices.length > 0 || savedLevel}
				<fieldset class="inner" data-testid="level-filter">
					<legend>Level</legend>
					<div class="grid">
						<div class="field">
							<label for="{id}-level-field">Field</label>
							<select id="{id}-level-field" bind:value={levelKey} onchange={levelFieldChanged}>
								<option value="">Any level</option>
								{#each levelChoices as c (levelKeyOf(c.profile, c.field))}
									<option value={levelKeyOf(c.profile, c.field)}>{c.agency}: {c.label}</option>
								{/each}
								{#if savedLevel && levelGone}
									<option value={levelKeyOf(savedLevel.profile, savedLevel.field)}
										>{savedLevel.profile}: {savedLevel.field} (not in the profiles now)</option
									>
								{/if}
							</select>
						</div>
						{#if levelChoice}
							<div class="field">
								<label for="{id}-level-at">At or above</label>
								<select id="{id}-level-at" bind:value={atLeast}>
									{#each levelChoice.levels as l (l.value)}
										<option value={l.value}>{l.label}</option>
									{/each}
								</select>
							</div>
						{/if}
					</div>
					<p class="help">
						With a field chosen, only that agency's pages at the chosen level or a higher one are sent: not its pages with no level or
						another one, and not other agencies' pages.
					</p>
				</fieldset>
			{/if}
			{#if admin}
				<fieldset class="inner">
					<legend>Flags (admins only)</legend>
					<div class="checks">
						<label class="check"><input type="checkbox" bind:checked={parseFailed} /> parse_failed</label>
						<label class="check"><input type="checkbox" bind:checked={addressUnsplit} /> address_unsplit</label>
						<label class="check"><input type="checkbox" bind:checked={locationMismatch} /> location_mismatch</label>
					</div>
				</fieldset>
			{/if}
		</fieldset>

		<fieldset>
			<legend>Content</legend>
			<div class="stacked">
				<label class="check"><input type="radio" bind:group={content} value="full" /> Full: the text and every field</label>
				<label class="check"
					><input type="radio" bind:group={content} value="summary" /> Summary: alias, event type, priority, time and job number. No text
					or address</label
				>
			</div>
			{#if presets.includes(kind) && content === 'full'}
				<p class="help warn">{kindLabels[kind]} is a third party: Full content sends it the page text and address.</p>
			{/if}
		</fieldset>
	{/if}

	<fieldset>
		<legend>{kindLabels[kind]}</legend>
		{#if kind === 'webhook'}
			<div class="field">
				<label for={f('url')}>URL {#if secretMarker('url')}<span class="required">{secretMarker('url')}</span>{/if}</label>
				<input id={f('url')} type="url" inputmode="url" autocomplete="off" autocapitalize="off" spellcheck="false" bind:value={secrets.url} required={secretNeeded('url')} placeholder={saved('url') || 'https://'} aria-invalid={fc.invalid(f('url'))} aria-describedby={fc.describedBy(f('url'), f('url-help'))} />
				<FieldError check={fc} id={f('url')} />
				<p id="{id}-url-help" class="help">
					{#if existing?.config.target}Saved: {existing.config.target}/…{/if}
					The URL is kept secret once saved. Use http:// only on your own network.
				</p>
			</div>
			<div class="grid">
				<div class="field">
					<label for="{id}-hname">Auth header name</label>
					<input id="{id}-hname" bind:value={config.auth_header} placeholder="Authorization" autocomplete="off" autocapitalize="off" spellcheck="false" />
				</div>
				<div class="field">
					<label for={f('hvalue')}>Auth header value {#if headerValueNeeded}<span class="required">needed with a header name</span>{/if}</label>
					<input id={f('hvalue')} type="password" autocomplete="off" bind:value={secrets.auth_value} required={headerValueNeeded} data-problem="Give the header a value, or clear its name." placeholder={saved('auth_value')} aria-invalid={fc.invalid(f('hvalue'))} aria-describedby={fc.describedBy(f('hvalue'))} />
					<FieldError check={fc} id={f('hvalue')} />
				</div>
			</div>
		{:else if kind === 'mqtt'}
			<div class="field">
				<label for={f('broker')}>Broker <span class="required">required</span></label>
				<input id={f('broker')} inputmode="url" autocomplete="off" autocapitalize="off" spellcheck="false" bind:value={config.broker} required placeholder="mqtt://broker.example.org or mqtts://…:8883" aria-invalid={fc.invalid(f('broker'))} aria-describedby={fc.describedBy(f('broker'))} />
				<FieldError check={fc} id={f('broker')} />
			</div>
			<div class="field">
				<label for="{id}-topic">Topic</label>
				<input id="{id}-topic" autocomplete="off" autocapitalize="off" spellcheck="false" bind:value={config.topic} placeholder={existing ? '' : 'pagerarchive/<made for you>'} />
			</div>
			<div class="grid">
				<div class="field">
					<label for="{id}-user">User name</label>
					<input id="{id}-user" bind:value={config.username} autocomplete="off" autocapitalize="off" spellcheck="false" />
				</div>
				<div class="field">
					<label for="{id}-pass">Password</label>
					<input id="{id}-pass" type="password" autocomplete="off" bind:value={secrets.password} placeholder={saved('password')} />
				</div>
			</div>
			<div class="field">
				<label for="{id}-ca">CA certificate</label>
				<textarea id="{id}-ca" rows="3" autocapitalize="off" spellcheck="false" bind:value={config.ca_cert} placeholder="-----BEGIN CERTIFICATE-----" aria-describedby="{id}-ca-help"></textarea>
				<p id="{id}-ca-help" class="help">For mqtts:// only.</p>
			</div>
			<label class="check"><input type="checkbox" bind:checked={config.ha_discovery} /> Home Assistant discovery: an event entity for this integration</label>
			{#if config.ha_discovery}
				<div class="field">
					<label for="{id}-prefix">Discovery prefix</label>
					<input id="{id}-prefix" autocomplete="off" autocapitalize="off" spellcheck="false" bind:value={config.ha_prefix} placeholder="homeassistant" />
				</div>
			{/if}
		{:else if kind === 'discord'}
			<div class="field">
				<label for={f('durl')}>Webhook URL {#if secretMarker('url')}<span class="required">{secretMarker('url')}</span>{/if}</label>
				<input id={f('durl')} type="url" inputmode="url" autocomplete="off" autocapitalize="off" spellcheck="false" bind:value={secrets.url} required={secretNeeded('url')} placeholder={saved('url') || 'https://discord.com/api/webhooks/…'} aria-invalid={fc.invalid(f('durl'))} aria-describedby={fc.describedBy(f('durl'), f('durl-help'))} />
				<FieldError check={fc} id={f('durl')} />
				<p id="{id}-durl-help" class="help">In Discord: Server settings → Integrations → Webhooks → Copy webhook URL.</p>
			</div>
		{:else if kind === 'telegram'}
			<div class="grid">
				<div class="field">
					<label for={f('bot')}>Bot token {#if secretMarker('bot_token')}<span class="required">{secretMarker('bot_token')}</span>{/if}</label>
					<input id={f('bot')} type="password" autocomplete="off" bind:value={secrets.bot_token} required={secretNeeded('bot_token')} placeholder={saved('bot_token')} aria-invalid={fc.invalid(f('bot'))} aria-describedby={fc.describedBy(f('bot'))} />
					<FieldError check={fc} id={f('bot')} />
				</div>
				<div class="field">
					<label for={f('chat')}>Chat ID <span class="required">required</span></label>
					<input id={f('chat')} autocomplete="off" autocapitalize="off" spellcheck="false" bind:value={config.chat_id} required placeholder="-1001234567890 or @channel" aria-invalid={fc.invalid(f('chat'))} aria-describedby={fc.describedBy(f('chat'))} />
					<FieldError check={fc} id={f('chat')} />
				</div>
			</div>
		{:else if kind === 'pushover'}
			<div class="grid">
				<div class="field">
					<label for={f('app')}>Application token {#if secretMarker('app_token')}<span class="required">{secretMarker('app_token')}</span>{/if}</label>
					<input id={f('app')} type="password" autocomplete="off" bind:value={secrets.app_token} required={secretNeeded('app_token')} placeholder={saved('app_token')} aria-invalid={fc.invalid(f('app'))} aria-describedby={fc.describedBy(f('app'))} />
					<FieldError check={fc} id={f('app')} />
				</div>
				<div class="field">
					<label for={f('ukey')}>User key {#if secretMarker('user_key')}<span class="required">{secretMarker('user_key')}</span>{/if}</label>
					<input id={f('ukey')} type="password" autocomplete="off" bind:value={secrets.user_key} required={secretNeeded('user_key')} placeholder={saved('user_key')} aria-invalid={fc.invalid(f('ukey'))} aria-describedby={fc.describedBy(f('ukey'))} />
					<FieldError check={fc} id={f('ukey')} />
				</div>
			</div>
			<div class="field">
				<label for="{id}-pprio">Priority</label>
				<select id="{id}-pprio" bind:value={priority}>
					<option value="page">Follow the page: high for Emergency, normal otherwise</option>
					<option value="1">High</option>
					<option value="0">Normal</option>
					<option value="-1">Low</option>
					<option value="-2">Lowest</option>
				</select>
			</div>
		{:else if kind === 'ntfy'}
			<div class="grid">
				<div class="field">
					<label for="{id}-server">Server</label>
					<input id={f('server')} type="url" inputmode="url" autocomplete="off" autocapitalize="off" spellcheck="false" bind:value={config.server} placeholder="https://ntfy.sh" aria-invalid={fc.invalid(f('server'))} aria-describedby={fc.describedBy(f('server'))} />
					<FieldError check={fc} id={f('server')} />
				</div>
				<div class="field">
					<label for={f('ntopic')}>Topic {#if secretMarker('topic')}<span class="required">{secretMarker('topic')}</span>{/if}</label>
					<input id={f('ntopic')} type="password" autocomplete="off" bind:value={secrets.topic} required={secretNeeded('topic')} placeholder={saved('topic')} aria-invalid={fc.invalid(f('ntopic'))} aria-describedby={fc.describedBy(f('ntopic'))} />
					<FieldError check={fc} id={f('ntopic')} />
				</div>
				<div class="field">
					<label for="{id}-ntoken">Access token</label>
					<input id="{id}-ntoken" type="password" autocomplete="off" bind:value={secrets.access_token} placeholder={saved('access_token')} />
				</div>
				<div class="field">
					<label for="{id}-nprio">Priority</label>
					<select id="{id}-nprio" bind:value={priority}>
						<option value="page">Follow the page: high for Emergency, default otherwise</option>
						<option value="4">High</option>
						<option value="3">Default</option>
						<option value="2">Low</option>
						<option value="1">Min</option>
					</select>
				</div>
			</div>
		{/if}
	</fieldset>

	{#if error}<p class="error" role="alert">{error}</p>{/if}
	<div class="actions">
		<button type="submit" disabled={saving}>{saving ? 'Saving…' : existing ? 'Save changes' : 'Save integration'}</button>
		<button type="button" class="secondary" onclick={oncancel}>Cancel</button>
	</div>
</form>

<style>
	.integration-form {
		margin: var(--space-md) 0;
		padding: var(--space-md);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius-badge);
		background: var(--surface);
		max-width: var(--measure);
	}
	h2 {
		font-size: var(--text-lg);
		margin: 0 0 var(--space-md);
	}
	fieldset {
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius-small);
		margin: var(--space-md) 0;
		padding: var(--space-sm) var(--space-md);
		min-width: 0;
	}
	fieldset.inner {
		border: 0;
		padding: 0;
		margin: var(--space-sm) 0;
	}
	legend {
		font-weight: var(--weight-strong);
		padding: 0 var(--space-2xs);
	}
	.field {
		margin: var(--space-sm) 0;
	}
	.field label {
		display: block;
	}
	/* Side by side where there's room; one column, never wider than the form,
	   where there isn't (a phone). */
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(var(--card-min), 100%), 1fr));
		gap: 0 var(--space-md);
	}
	.checks {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2xs) var(--space-lg);
	}
	/* One option per line, each radio directly under the one above. */
	.stacked {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: var(--space-xs);
	}
	.check {
		display: inline-flex;
		align-items: baseline;
		gap: var(--space-xs);
	}
	.check input {
		flex: none;
	}
	.help {
		color: var(--muted);
		font-size: var(--text-sm);
		margin: var(--space-2xs) 0;
	}
	.help.warn {
		color: var(--warn-fg);
		background: var(--warn-bg);
		padding: var(--space-2xs) var(--space-sm);
		border-radius: var(--radius-small);
	}
	textarea {
		font: inherit;
		font-family: var(--font-mono);
		font-size: var(--text-sm);
		color: inherit;
		background: var(--bg);
		border: var(--border-width) solid var(--muted);
		border-radius: var(--radius-small);
		padding: var(--space-xs) var(--space-sm);
		width: 100%;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
	}
	/* A phone (R81) or a finger (owner, 2026-11-14): the form keeps its width
	   for the fields, and each radio and checkbox, with its label, is a whole
	   target to tap (R82c). The box itself keeps its size. */
	@media (max-width: 47.99rem) {
		.integration-form {
			padding: var(--space-sm);
		}
		fieldset {
			padding: var(--space-2xs) var(--space-sm) var(--space-sm);
		}
	}
	@media (max-width: 47.99rem), (pointer: coarse) {
		.check {
			min-height: var(--target);
			align-items: center;
		}
		.checks {
			gap: 0 var(--space-lg);
		}
		.stacked {
			gap: 0;
		}
	}
</style>
