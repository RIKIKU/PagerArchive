<script lang="ts">
	// R51: the private hosts and ranges integrations may reach. Everything
	// else private, loopback or link-local is refused.
	import { api } from '#lib/api.ts';

	let entries = $state('');
	let error = $state('');
	let notice = $state('');
	let saving = $state(false);

	api
		.allowList()
		.then((a) => (entries = a.entries.join('\n')))
		.catch((e) => (error = e.message));

	async function saveAllowList(ev: SubmitEvent) {
		ev.preventDefault();
		error = notice = '';
		saving = true;
		try {
			const a = await api.setAllowList(entries.split(/[\s,]+/).filter(Boolean));
			entries = a.entries.join('\n');
			notice = a.entries.length ? `Allowed: ${a.entries.join(', ')}.` : 'No private addresses are allowed.';
		} catch (e) {
			error = (e as Error).message;
		} finally {
			saving = false;
		}
	}
</script>

<svelte:head><title>Integration addresses · PagerArchive</title></svelte:head>

<h1>Integration addresses</h1>
<p class="note">
	Integrations can't reach private, loopback, link-local or other reserved addresses unless they're listed here, for
	example your Home Assistant. One per line: an address (192.168.1.10), a range (192.168.1.0/24) or a host name
	(homeassistant.lan).
</p>
{#if error}<p class="error" role="alert">{error}</p>{/if}
{#if notice}<p class="ok" role="status">{notice}</p>{/if}
<form onsubmit={saveAllowList}>
	<label for="hosts">Allowed hosts and ranges</label>
	<textarea id="hosts" rows="6" bind:value={entries} spellcheck="false"></textarea>
	<button type="submit" disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
</form>

<style>
	.note {
		margin: 0 0 var(--space-sm);
		color: var(--muted);
		max-width: var(--measure);
	}
	.ok,
	.error {
		margin: 0 0 var(--space-sm);
	}
	form {
		margin: 0;
		max-width: var(--measure-narrow);
		display: grid;
		gap: var(--space-sm);
		justify-items: start;
	}
	textarea {
		font-family: var(--font-mono);
		font-size: var(--text-sm);
		color: inherit;
		background: var(--bg);
		border: var(--border-width) solid var(--muted);
		border-radius: var(--radius-small);
		padding: var(--space-xs) var(--space-sm);
		width: 100%;
	}
</style>
