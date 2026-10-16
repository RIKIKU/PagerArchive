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
		type Meta
	} from './api.ts';
	import { kindLabels, presets } from './integrations.ts';
	import SelectionField from './SelectionField.svelte';

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
	let coveredBy: Record<string, string> = $state({ ...(init?.covered_by ?? {}) });
	let categories: string[] = $state([...(init?.filter.categories ?? [])]);
	let codes = $state((init?.filter.codes ?? []).join(', '));
	let priorities: string[] = $state([...(init?.filter.priorities ?? [])]);
	let text = $state(init?.filter.text ?? '');
	let group = $state(init?.filter.group ?? '');
	let agency = $state(init?.filter.agency ?? '');
	let job = $state(init?.filter.job ?? '');
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
				group,
				agency,
				job,
				parse_failed: admin && parseFailed,
				address_unsplit: admin && addressUnsplit,
				location_mismatch: admin && locationMismatch
			},
			config: { ...config, target: undefined, priority: priority === 'page' ? null : Number(priority) },
			secrets: Object.fromEntries(Object.entries(secrets).filter(([, v]) => v)) as IntegrationSecrets
		};
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

<form class="integration-form" onsubmit={submit} aria-labelledby="{id}-heading">
	<h2 id="{id}-heading">{existing ? `Edit “${existing.name}”` : 'New integration'}</h2>

	<div class="field">
		<label for="{id}-name">Name</label>
		<input id="{id}-name" bind:value={name} required maxlength="80" />
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
		<legend>Which pages (at least one capcode or alias)</legend>
		<SelectionField id="{id}-selection" bind:capcodes bind:aliases={chosenAliases} bind:coveredBy />
	</fieldset>

	<fieldset>
		<legend>Only these (optional)</legend>
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
			<input id="{id}-codes" bind:value={codes} placeholder="HSF1, GRS3" />
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
				<input id="{id}-text" bind:value={text} />
			</div>
			<div class="field">
				<label for="{id}-group">Group</label>
				<input id="{id}-group" bind:value={group} />
			</div>
			<div class="field">
				<label for="{id}-agency">Agency</label>
				<input id="{id}-agency" bind:value={agency} />
			</div>
			<div class="field">
				<label for="{id}-job">Job number</label>
				<input id="{id}-job" bind:value={job} />
			</div>
		</div>
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

	<fieldset>
		<legend>{kindLabels[kind]}</legend>
		{#if kind === 'webhook'}
			<div class="field">
				<label for="{id}-url">URL</label>
				<input id="{id}-url" type="url" bind:value={secrets.url} required={!existing?.secrets_set.url} placeholder={saved('url') || 'https://'} aria-describedby="{id}-url-help" />
				<p id="{id}-url-help" class="help">
					{#if existing?.config.target}Saved: {existing.config.target}/…{/if}
					The URL is kept secret once saved. Use http:// only on your own network.
				</p>
			</div>
			<div class="grid">
				<div class="field">
					<label for="{id}-hname">Auth header name (optional)</label>
					<input id="{id}-hname" bind:value={config.auth_header} placeholder="Authorization" />
				</div>
				<div class="field">
					<label for="{id}-hvalue">Auth header value</label>
					<input id="{id}-hvalue" type="password" autocomplete="off" bind:value={secrets.auth_value} placeholder={saved('auth_value')} />
				</div>
			</div>
		{:else if kind === 'mqtt'}
			<div class="field">
				<label for="{id}-broker">Broker</label>
				<input id="{id}-broker" bind:value={config.broker} required placeholder="mqtt://broker.example.org or mqtts://…:8883" />
			</div>
			<div class="field">
				<label for="{id}-topic">Topic</label>
				<input id="{id}-topic" bind:value={config.topic} placeholder={existing ? '' : 'pagerarchive/<made for you>'} />
			</div>
			<div class="grid">
				<div class="field">
					<label for="{id}-user">User name (optional)</label>
					<input id="{id}-user" bind:value={config.username} autocomplete="off" />
				</div>
				<div class="field">
					<label for="{id}-pass">Password</label>
					<input id="{id}-pass" type="password" autocomplete="off" bind:value={secrets.password} placeholder={saved('password')} />
				</div>
			</div>
			<div class="field">
				<label for="{id}-ca">CA certificate (optional, mqtts:// only)</label>
				<textarea id="{id}-ca" rows="3" bind:value={config.ca_cert} placeholder="-----BEGIN CERTIFICATE-----"></textarea>
			</div>
			<label class="check"><input type="checkbox" bind:checked={config.ha_discovery} /> Home Assistant discovery: an event entity for this integration</label>
			{#if config.ha_discovery}
				<div class="field">
					<label for="{id}-prefix">Discovery prefix</label>
					<input id="{id}-prefix" bind:value={config.ha_prefix} placeholder="homeassistant" />
				</div>
			{/if}
		{:else if kind === 'discord'}
			<div class="field">
				<label for="{id}-durl">Webhook URL</label>
				<input id="{id}-durl" type="url" bind:value={secrets.url} required={!existing?.secrets_set.url} placeholder={saved('url') || 'https://discord.com/api/webhooks/…'} aria-describedby="{id}-durl-help" />
				<p id="{id}-durl-help" class="help">In Discord: Server settings → Integrations → Webhooks → Copy webhook URL.</p>
			</div>
		{:else if kind === 'telegram'}
			<div class="grid">
				<div class="field">
					<label for="{id}-bot">Bot token</label>
					<input id="{id}-bot" type="password" autocomplete="off" bind:value={secrets.bot_token} required={!existing?.secrets_set.bot_token} placeholder={saved('bot_token')} />
				</div>
				<div class="field">
					<label for="{id}-chat">Chat ID</label>
					<input id="{id}-chat" bind:value={config.chat_id} required placeholder="-1001234567890 or @channel" />
				</div>
			</div>
		{:else if kind === 'pushover'}
			<div class="grid">
				<div class="field">
					<label for="{id}-app">Application token</label>
					<input id="{id}-app" type="password" autocomplete="off" bind:value={secrets.app_token} required={!existing?.secrets_set.app_token} placeholder={saved('app_token')} />
				</div>
				<div class="field">
					<label for="{id}-ukey">User key</label>
					<input id="{id}-ukey" type="password" autocomplete="off" bind:value={secrets.user_key} required={!existing?.secrets_set.user_key} placeholder={saved('user_key')} />
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
					<input id="{id}-server" type="url" bind:value={config.server} placeholder="https://ntfy.sh" />
				</div>
				<div class="field">
					<label for="{id}-ntopic">Topic</label>
					<input id="{id}-ntopic" type="password" autocomplete="off" bind:value={secrets.topic} required={!existing?.secrets_set.topic} placeholder={saved('topic')} />
				</div>
				<div class="field">
					<label for="{id}-ntoken">Access token (optional)</label>
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
		margin: 0.75rem;
		padding: 0.75rem;
		border: 1px solid var(--border);
		border-radius: 0.4rem;
		background: var(--surface);
		max-width: 44rem;
	}
	h2 {
		font-size: var(--text-lg);
		margin: 0 0 0.75rem;
	}
	fieldset {
		border: 1px solid var(--border);
		border-radius: 0.3rem;
		margin: 0.75rem 0;
		padding: 0.5rem 0.75rem;
		min-width: 0;
	}
	fieldset.inner {
		border: 0;
		padding: 0;
		margin: 0.5rem 0;
	}
	legend {
		font-weight: 600;
		padding: 0 0.25rem;
	}
	.field {
		margin: 0.5rem 0;
	}
	.field label {
		display: block;
		margin-bottom: 0.2rem;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr));
		gap: 0 0.75rem;
	}
	.checks {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem 1rem;
	}
	/* One option per line, each radio directly under the one above. */
	.stacked {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.35rem;
	}
	.check {
		display: inline-flex;
		align-items: baseline;
		gap: 0.4rem;
	}
	.check input {
		flex: none;
	}
	.help {
		color: var(--muted);
		font-size: var(--text-sm);
		margin: 0.25rem 0;
	}
	.help.warn {
		color: var(--warn-fg);
		background: var(--warn-bg);
		padding: 0.25rem 0.5rem;
		border-radius: 0.25rem;
	}
	textarea {
		font: inherit;
		font-family: var(--mono);
		font-size: var(--text-sm);
		color: inherit;
		background: var(--bg);
		border: 1px solid var(--muted);
		border-radius: 0.3rem;
		padding: 0.35rem 0.5rem;
		width: 100%;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}
</style>
