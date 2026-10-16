<script lang="ts">
	// R47-R51c: the signed-in user's own integrations. Nobody else sees them;
	// admins only see that they exist.
	import { api, type AliasBrief, type Delivery, type Integration, type Meta, type TestResult } from '#lib/api.ts';
	import { dateTime, isoAttr } from '#lib/format.ts';
	import IntegrationForm from '#lib/IntegrationForm.svelte';
	import { deliveryLabels, kindLabels, statusLabel } from '#lib/integrations.ts';

	let meta: Meta | undefined = $state();
	let aliases: AliasBrief[] = $state([]);
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
	api.aliases().then((a) => (aliases = a)).catch(() => {});
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
		const parts = [...i.aliases.map((a) => a.name), ...i.capcodes];
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

{#if enabled && meta && editing === undefined}
	<div class="actions top">
		<button type="button" onclick={() => ((editing = 'new'), (notice = ''))}>New integration</button>
	</div>
{/if}
{#if meta && editing === 'new'}
	<IntegrationForm {meta} {aliases} onsaved={saved} oncancel={() => (editing = undefined)} />
{/if}

{#if loaded && list.length === 0 && editing !== 'new'}
	<p class="status">You have no integrations yet.</p>
{/if}

<ul class="cards">
	{#each list as i (i.id)}
		<li data-testid="integration">
			{#if meta && editing === i.id}
				<IntegrationForm existing={i} {meta} {aliases} onsaved={saved} oncancel={() => (editing = undefined)} />
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
									<a href="/messages/{d.message_id}"><time datetime={isoAttr(d.sent_at)}>{dateTime(d.sent_at, tz)}</time></a>
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

<style>
	.intro {
		margin: 0 0.75rem;
		max-width: 44rem;
		color: var(--muted);
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin: 0.5rem 0;
	}
	.actions.top {
		margin: 0.75rem;
	}
	.cards {
		list-style: none;
		padding: 0;
		margin: 0;
	}
	.cards > li {
		border-bottom: 1px solid var(--border);
		padding: 0.75rem;
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 0.25rem 0.75rem;
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
		border: 1px solid var(--border);
		border-radius: 1rem;
		padding: 0 0.5rem;
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
		gap: 0.15rem 1rem;
		margin: 0.5rem 0;
	}
	dt {
		color: var(--muted);
	}
	dd {
		margin: 0;
		overflow-wrap: anywhere;
	}
	.mono {
		font-family: var(--mono);
		font-size: var(--text-sm);
	}
	.warn {
		color: var(--warn-fg);
	}
	.answer {
		border-left: 4px solid var(--accent);
		background: var(--surface);
		padding: 0.25rem 0.75rem;
		margin: 0.5rem 0;
	}
	.answer p {
		margin: 0.25rem 0;
	}
	pre {
		font-family: var(--mono);
		font-size: var(--text-sm);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		margin: 0.25rem 0;
		max-height: 12rem;
		overflow-y: auto;
	}
	.deliveries {
		list-style: none;
		padding: 0;
		margin: 0.25rem 0;
	}
	.deliveries li {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem 0.75rem;
		align-items: baseline;
		padding: 0.25rem 0;
		border-top: 1px solid var(--border);
	}
	.muted {
		color: var(--muted);
		font-size: var(--text-sm);
	}
</style>
