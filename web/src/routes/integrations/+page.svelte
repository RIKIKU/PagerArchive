<script lang="ts">
	// R47-R51c: the signed-in user's own integrations. Nobody else sees them;
	// admins only see that they exist.
	import { api, type Delivery, type Integration, type Meta, type TestResult, largeEventLevels } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';
	import IntegrationForm from '#lib/IntegrationForm.svelte';
	import Icon from '#lib/Icon.svelte';
	import { deliveryLabels, kindLabels, notice as convenience, statusLabel } from '#lib/integrations.ts';
	import { screen } from '#lib/screen.svelte.ts';

	let meta: Meta | undefined = $state();
	let list: Integration[] = $state([]);
	let enabled = $state(true);
	let loaded = $state(false);
	let error = $state('');
	let notice = $state('');
	let editing: number | 'new' | undefined = $state();
	let tests: Record<number, TestResult | { error: string }> = $state({});
	let testing: number | undefined = $state();
	let open: Record<number, Delivery[] | undefined> = $state({});
	let confirmDelete: number | undefined = $state();

	const tz = $derived(meta?.time_zone ?? 'UTC');

	api.meta().then((m) => (meta = m));
	function load() {
		api
			.integrations()
			.then((r) => {
				list = r.integrations;
				enabled = r.enabled;
				loaded = true;
			})
			.catch((e) => (error = e.message));
	}
	load();

	function saved(i: Integration) {
		notice = `Saved “${i.name}”.` + (i.discovery ? ` ${i.discovery.message}` : '');
		editing = undefined;
		load();
	}

	async function act(fn: () => Promise<unknown>, done: string) {
		error = notice = '';
		try {
			await fn();
			notice = done;
			load();
		} catch (e) {
			error = (e as Error).message;
		}
	}

	async function test(i: Integration) {
		testing = i.id;
		try {
			tests[i.id] = await api.testIntegration(i.id);
		} catch (e) {
			tests[i.id] = { error: (e as Error).message };
		} finally {
			testing = undefined;
		}
	}

	async function toggleDeliveries(i: Integration) {
		if (open[i.id]) {
			open[i.id] = undefined;
			return;
		}
		try {
			open[i.id] = await api.deliveries(i.id);
		} catch (e) {
			error = (e as Error).message;
		}
	}

	function selection(i: Integration): string {
		if (i.large_events) return `Large events at level ${largeEventLevels.find((l) => l.level === i.large_events)?.label ?? i.large_events}`;
		const parts = [...i.aliases.map((a) => a.name), ...i.capcodes.map((c) => (i.covered_by[c] ? `${c} (in ${i.covered_by[c]})` : c))];
		return parts.length > 4 ? `${parts.slice(0, 4).join(', ')} and ${parts.length - 4} more` : parts.join(', ');
	}

	function destination(i: Integration): string {
		switch (i.kind) {
			case 'webhook':
				return i.config.target ? `${i.config.target}/…` : '';
			case 'mqtt':
				return `${i.config.broker ?? ''} · ${i.config.topic ?? ''}${i.config.ha_discovery ? ' · Home Assistant' : ''}`;
			case 'telegram':
				return `chat ${i.config.chat_id ?? ''}`;
			case 'ntfy':
				return i.config.server ?? '';
			default:
				return '';
		}
	}
</script>

<svelte:head><title>Integrations · PagerArchive</title></svelte:head>

<h1>Integrations</h1>
<p class="convenience" data-testid="integrations-notice">{convenience}</p>
<p class="intro">
	Send new pages to your own systems as they arrive: a webhook, MQTT (with Home Assistant discovery), Discord, Telegram,
	Pushover or ntfy. Only you can see your integrations. A page is sent within seconds, or not at all once it's 5 minutes
	old.
</p>
{#if !enabled}
	<p class="ok" role="status">Integrations are off on this server until its operator sets INTEGRATION_SECRETS_KEY.</p>
{/if}
{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if notice}<p class="ok" role="status">{notice}</p>{/if}

{#if screen.phone}
	<!-- R81: on a phone, a Pause/Resume switch for each, and nothing else;
	     setting one up or editing it is for a desktop. -->
	{#if loaded && list.length === 0}<p class="status">You have no integrations yet.</p>{/if}
	<ul class="switches" data-testid="integration-switches">
		{#each list as i (i.id)}
			{@const on = i.status === 'active'}
			<li>
				<span class="name">{i.name}</span>
				<button
					class="switch"
					type="button"
					role="switch"
					aria-checked={on}
					aria-label="{i.name}: sending"
					disabled={!enabled || (!on && i.paused_by === 'admin')}
					onclick={() =>
						act(() => (on ? api.pauseIntegration(i.id) : api.resumeIntegration(i.id)), `${on ? 'Paused' : 'Resumed'} “${i.name}”.`)}
					><span class="knob" aria-hidden="true"></span><span data-testid="integration-status">{statusLabel(i)}</span></button
				>
			</li>
		{/each}
	</ul>
{:else}
	{#if enabled && meta && editing === undefined}
		<div class="actions top">
			<button type="button" onclick={() => ((editing = 'new'), (notice = ''))}><Icon name="integrations" />New integration</button>
		</div>
	{/if}
	{#if meta && editing === 'new'}
		<IntegrationForm {meta} onsaved={saved} oncancel={() => (editing = undefined)} />
	{/if}

	{#if loaded && list.length === 0 && editing !== 'new'}
		<p class="status">You have no integrations yet.</p>
	{/if}
{/if}

{#if !screen.phone}
<ul class="cards">
	{#each list as i (i.id)}
		<li data-testid="integration">
			{#if meta && editing === i.id}
				<IntegrationForm existing={i} {meta} onsaved={saved} oncancel={() => (editing = undefined)} />
			{:else}
				<div class="head">
					<h2>{i.name}</h2>
					<span class="kind">{kindLabels[i.kind]} · {i.content === 'full' ? 'Full' : 'Summary'}</span>
					<span class="state" class:paused={i.status === 'paused'} data-testid="integration-status">{statusLabel(i)}</span>
				</div>
				<dl>
					<dt>Pages from</dt>
					<dd>{selection(i)}</dd>
					{#if destination(i)}
						<dt>Sends to</dt>
						<dd class="mono">{destination(i)}</dd>
					{/if}
					<dt>Last delivered</dt>
					<dd>
						{#if i.last_delivered_at}<time datetime={isoAttr(i.last_delivered_at)}>{dateTime(i.last_delivered_at, tz)}</time>{:else}Never{/if}
					</dd>
					{#if i.last_error}
						<dt>Last error</dt>
						<dd data-testid="last-error">
							{i.last_error}{#if i.last_error_at}, <time datetime={isoAttr(i.last_error_at)}>{dateTime(i.last_error_at, tz)}</time>{/if}
						</dd>
					{/if}
					{#if i.secrets_error}
						<dt>Secrets</dt>
						<dd class="warn">{i.secrets_error}</dd>
					{/if}
				</dl>
				<div class="actions">
					<button type="button" onclick={() => test(i)} disabled={testing === i.id || !enabled}>
						{testing === i.id ? 'Sending…' : 'Send test'}
					</button>
					<button type="button" class="secondary" onclick={() => ((editing = i.id), (notice = ''))} disabled={!enabled}>Edit</button>
					{#if i.status === 'active'}
						<button type="button" class="secondary" onclick={() => act(() => api.pauseIntegration(i.id), `Paused “${i.name}”.`)}>Pause</button>
					{:else if i.paused_by !== 'admin'}
						<button type="button" class="secondary" onclick={() => act(() => api.resumeIntegration(i.id), `Resumed “${i.name}”.`)}>Resume</button>
					{/if}
					<button type="button" class="secondary" onclick={() => toggleDeliveries(i)} aria-expanded={!!open[i.id]}>
						{open[i.id] ? 'Hide deliveries' : 'Deliveries'}
					</button>
					{#if confirmDelete === i.id}
						<button type="button" class="danger" onclick={() => act(() => api.deleteIntegration(i.id), `Deleted “${i.name}”.`)}>Yes, delete it</button>
						<button type="button" class="secondary" onclick={() => (confirmDelete = undefined)}>Keep it</button>
					{:else}
						<button type="button" class="secondary" onclick={() => (confirmDelete = i.id)}>Delete…</button>
					{/if}
				</div>
				{#if tests[i.id]}
					{@const t = tests[i.id]}
					<div class="answer" role="status" data-testid="test-answer">
						{#if 'took_ms' in t}
							<p>
								<strong>{t.ok ? 'Delivered.' : 'Not delivered.'}</strong>
								The endpoint answered {t.result}{t.error && t.error !== t.result ? `: ${t.error}` : ''} in {t.took_ms} ms.
							</p>
							{#if t.body}<pre>{t.body}</pre>{/if}
						{:else}
							<p class="error">{t.error}</p>
						{/if}
					</div>
				{/if}
				{#if open[i.id]}
					{@const ds = open[i.id] ?? []}
					<h3 class="visually-hidden">Recent deliveries of {i.name}</h3>
					{#if ds.length === 0}
						<p class="status">No pages yet.</p>
					{:else}
						<ul class="deliveries">
							{#each ds as d (d.message_id)}
								<li>
									<a href={d.event_id ? `/large-events/${d.event_id}` : `/messages/${d.message_id}`}><time datetime={isoAttr(d.sent_at)}>{dateTime(d.sent_at, tz)}</time></a>
									<span class="dstate {d.status}">{deliveryLabels[d.status]}</span>
									<span class="muted"
										>{d.attempts} attempt{d.attempts === 1 ? '' : 's'}{#if d.last_error && d.status !== 'delivered'} · {d.last_error}{:else if d.last_result} · {d.last_result}{/if}</span
									>
								</li>
							{/each}
						</ul>
					{/if}
				{/if}
			{/if}
		</li>
	{/each}
</ul>
{/if}

<style>
	.convenience {
		margin: 0 0 var(--space-md);
		max-width: var(--measure);
		padding: var(--space-sm) var(--space-md);
		background: var(--warn-bg);
		color: var(--warn-fg);
		border-radius: var(--radius-small);
		font-weight: var(--weight-strong);
	}
	.intro {
		margin: 0;
		max-width: var(--measure);
		color: var(--muted);
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
		margin: var(--space-md) 0 0;
	}
	.actions.top {
		margin: var(--space-lg) 0;
	}
	.cards,
	.switches {
		display: grid;
		gap: var(--space-md);
		list-style: none;
		padding: 0;
		margin: 0;
	}
	.cards > li {
		padding: var(--space-lg) var(--space-xl);
		background: var(--surface);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius);
	}
	.switches {
		margin-top: var(--space-lg);
	}
	.switches li {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-lg);
		padding: var(--space-sm) var(--space-lg);
		background: var(--surface);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius);
	}
	.name {
		font-weight: var(--weight-strong);
		overflow-wrap: anywhere;
	}
	.switch {
		flex: none;
		gap: var(--space-sm);
		padding: 0 var(--space-2xs);
		border: 0;
		background: transparent;
		color: var(--fg);
	}
	.switch:hover,
	.switch:active {
		background: transparent;
	}
	.knob {
		position: relative;
		flex: none;
		width: var(--switch-width);
		height: var(--switch-height);
		border-radius: var(--radius-pill);
		background: var(--control-border);
	}
	.knob::after {
		content: '';
		position: absolute;
		top: var(--switch-inset);
		left: var(--switch-inset);
		width: calc(var(--switch-height) - 2 * var(--switch-inset));
		height: calc(var(--switch-height) - 2 * var(--switch-inset));
		border-radius: var(--radius-round);
		background: var(--surface-2);
		transition: transform 0.15s;
	}
	.switch[aria-checked='true'] .knob {
		background: var(--accent);
	}
	.switch[aria-checked='true'] .knob::after {
		transform: translateX(calc(var(--switch-width) - var(--switch-height)));
	}
	.switch:hover .knob {
		box-shadow: 0 0 0 var(--space-2xs) var(--hover);
	}
	@media (prefers-reduced-motion: reduce) {
		.knob::after {
			transition: none;
		}
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: var(--space-2xs) var(--space-md);
	}
	h2 {
		font-size: var(--text-lg);
		margin: 0;
		overflow-wrap: anywhere;
	}
	.kind {
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.state,
	.dstate {
		font-size: var(--text-sm);
		border: var(--border-width) solid var(--border);
		border-radius: var(--radius-pill);
		padding: 0 var(--space-sm);
	}
	.state.paused,
	.dstate.failed,
	.dstate.too_old {
		background: var(--warn-bg);
		color: var(--warn-fg);
		border-color: var(--warn-bg);
	}
	dl {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: var(--space-3xs) var(--space-lg);
		margin: var(--space-sm) 0;
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
		overflow-wrap: anywhere;
	}
	.mono {
		font-family: var(--font-mono);
		font-size: var(--text-sm);
	}
	.warn {
		color: var(--warn-fg);
	}
	.answer {
		border-left: var(--rule-width) solid var(--accent);
		background: var(--surface);
		padding: var(--space-2xs) var(--space-md);
		margin: var(--space-sm) 0;
	}
	.answer p {
		margin: var(--space-2xs) 0;
	}
	pre {
		font-family: var(--font-mono);
		font-size: var(--text-sm);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		margin: var(--space-2xs) 0;
		max-height: var(--pre-height);
		overflow-y: auto;
	}
	.deliveries {
		list-style: none;
		padding: 0;
		margin: var(--space-2xs) 0;
	}
	.deliveries li {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2xs) var(--space-md);
		align-items: baseline;
		padding: var(--space-2xs) 0;
		border-top: var(--border-width) solid var(--border);
	}
	.muted {
		color: var(--muted);
		font-size: var(--text-sm);
	}
</style>
