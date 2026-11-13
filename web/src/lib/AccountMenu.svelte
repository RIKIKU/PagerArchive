<script lang="ts">
	// The account menu (R77a): who's signed in, their profile, the colour
	// theme (ADR 0015, decision 5), the look (R82d) and signing out.
	import { api, type Look, type Me } from './api.ts';
	import Icon from './Icon.svelte';

	let {
		me,
		theme = $bindable(),
		look = $bindable()
	}: { me: Me | undefined; theme: 'system' | 'light' | 'dark'; look: Look | undefined } = $props();

	let box: HTMLDivElement;
	let toggle: HTMLButtonElement;
	let open = $state(false);
	const uid = $props.id();
	const name = $derived(me?.name || me?.email || '');
	const initials = $derived(
		name
			.split(/[\s@.]+/)
			.filter(Boolean)
			.slice(0, 2)
			.map((w) => w[0].toUpperCase())
			.join('') || '?'
	);

	// R82d: a look applies at once (the layout puts it on the page's root)
	// and is saved on the account; if it can't be saved, the old one comes
	// back and the menu says so.
	let lookError = $state('');
	async function choose(next: Look) {
		const before = look;
		look = next;
		lookError = '';
		try {
			await api.setLook(next);
		} catch {
			look = before;
			lookError = "Couldn't save the look. Try again.";
		}
	}

	// Closes when focus or a click goes elsewhere, or on Escape.
	function outside(ev: Event) {
		if (open && !box.contains(ev.target as Node)) open = false;
	}
	function escape(ev: KeyboardEvent) {
		if (ev.key === 'Escape' && open) {
			open = false;
			toggle.focus();
		}
	}
</script>

<svelte:window onclick={outside} onfocusin={outside} onkeydown={escape} />

<div class="account" bind:this={box}>
	<button
		bind:this={toggle}
		class="ghost"
		type="button"
		aria-expanded={open}
		aria-controls="{uid}-panel"
		aria-label="Account menu{name ? `, ${name}` : ''}"
		onclick={() => (open = !open)}><span class="avatar" aria-hidden="true">{initials}</span></button
	>
	<div class="panel" id="{uid}-panel" hidden={!open}>
		{#if me}
			<p class="who"><span class="name">{me.name ?? me.email}</span>{#if me.name}<span class="email">{me.email}</span>{/if}</p>
		{/if}
		<a class="row" href="/account/profile" onclick={() => (open = false)}><Icon name="profile" />Profile</a>
		<label class="theme">
			<span class="row-label"><Icon name={theme === 'dark' ? 'dark' : theme === 'light' ? 'light' : 'auto'} />Colour theme</span>
			<select bind:value={theme}>
				<option value="system">Auto</option>
				<option value="light">Light</option>
				<option value="dark">Dark</option>
			</select>
		</label>
		<label class="theme">
			<span class="row-label"><Icon name="look" />Look</span>
			<select value={look ?? 'harbour'} disabled={!me} onchange={(e) => choose(e.currentTarget.value as Look)}>
				<option value="harbour">Harbour</option>
				<option value="console">Console</option>
			</select>
		</label>
		{#if lookError}<p class="error look-error" role="alert">{lookError}</p>{/if}
		<button class="row" type="button" onclick={() => api.signOut()}><Icon name="signout" />Sign out</button>
	</div>
</div>

<style>
	.account {
		position: relative;
	}
	.account > button {
		padding: 0;
	}
	.panel[hidden] {
		display: none;
	}
	.avatar {
		display: inline-grid;
		place-items: center;
		width: var(--avatar-size);
		height: var(--avatar-size);
		border-radius: var(--radius-round);
		background: var(--accent-soft);
		color: var(--accent-soft-fg);
		font-size: var(--text-xs);
		font-weight: var(--weight-strong);
	}
	.panel {
		position: absolute;
		right: 0;
		top: 100%;
		z-index: var(--layer-overlay);
		display: flex;
		flex-direction: column;
		gap: var(--space-2xs);
		min-width: var(--field-wide);
		margin-top: var(--space-2xs);
		padding: var(--space-sm);
		background: var(--surface-2);
		border: var(--border-width) solid var(--control-border);
		border-radius: var(--radius);
	}
	.who {
		display: flex;
		flex-direction: column;
		margin: 0;
		padding: var(--space-2xs) var(--space-md) var(--space-sm);
		border-bottom: var(--border-width) solid var(--border);
		overflow-wrap: anywhere;
	}
	.name {
		font-weight: var(--weight-strong);
	}
	.email {
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.row {
		display: flex;
		align-items: center;
		justify-content: flex-start;
		gap: var(--space-sm);
		width: 100%;
		min-height: var(--target);
		padding: 0 var(--space-md);
		border: 0;
		border-radius: var(--radius-small);
		background: transparent;
		color: var(--fg);
		font-weight: var(--weight-normal);
		text-decoration: none;
	}
	.row:hover {
		background: var(--hover);
	}
	.row:active {
		background: var(--accent-soft);
		color: var(--accent-soft-fg);
	}
	.theme {
		display: flex;
		flex-direction: column;
		gap: var(--space-2xs);
		padding: var(--space-xs) var(--space-md);
	}
	.look-error {
		margin: 0;
		padding: var(--space-2xs) var(--space-md);
		font-size: var(--text-sm);
	}
	.row-label {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		font-size: var(--text-sm);
	}
</style>
