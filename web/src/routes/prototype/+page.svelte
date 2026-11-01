<script lang="ts">
	// M13 prototypes (ADR 0015), throwaway: one structure (R77-R81a) in three
	// looks, on made-up example data. Not wired into the real pages. All state
	// is in the address, so every view can be linked and screenshotted:
	// look, theme (light, dark; none follows the system), role (user), menu
	// (shrunk), page, m (an open message), more (the phone's More menu) and
	// bare (no prototype controls).
	import './prototype.css';
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { date, time } from '#lib/format.ts';
	import Detail from './Detail.svelte';
	import Icon from './Icon.svelte';
	import Menu from './Menu.svelte';
	import Row from './Row.svelte';
	import { messages, tz } from './data.ts';
	import { crumbs, findEntry, findGroup, groups, looks, title, type LookId } from './looks.ts';

	const q = $derived(page.url.searchParams);
	const look = $derived((looks.find((l) => l.id === q.get('look'))?.id ?? 'ledger') as LookId);
	const theme = $derived(q.get('theme') === 'light' || q.get('theme') === 'dark' ? q.get('theme')! : 'auto');
	const admin = $derived(q.get('role') !== 'user');
	const shrunk = $derived(q.get('menu') === 'shrunk');
	const at = $derived(q.get('page') ?? 'messages');
	const open = $derived(messages.find((m) => String(m.id) === q.get('m')));
	const more = $derived(q.get('more') === '1');
	const bare = $derived(q.get('bare') === '1');

	/** This address with some parameters changed (null removes one). */
	function with_(changes: Record<string, string | null>): string {
		const u = new URLSearchParams(q.toString());
		for (const [k, v] of Object.entries(changes)) {
			if (v === null) u.delete(k);
			else u.set(k, v);
		}
		const s = u.toString();
		return s ? `?${s}` : '?';
	}
	const go = (id: string) => with_({ page: id === 'messages' ? null : id, m: null, more: null });

	$effect(() => {
		const root = document.documentElement;
		root.dataset.proto = look;
		if (theme === 'auto') delete root.dataset.protoTheme;
		else root.dataset.protoTheme = theme;
		return () => {
			delete root.dataset.proto;
			delete root.dataset.protoTheme;
		};
	});

	const where = $derived(crumbs(at, at === 'messages' && open ? `${open.alias ?? open.capcode}, ${date(open.sent_at, tz)} ${time(open.sent_at, tz)}` : undefined));
	const look_ = $derived(looks.find((l) => l.id === look)!);

	// R81: the phone's More menu holds the rest of R77's menu, less the
	// desktop-only pages (the print sheet, integration set-up, Administration).
	const phoneGroups = $derived(
		groups
			.filter((g) => !g.adminOnly && g.id !== 'albums')
			.map((g) => ({ ...g, entries: g.entries.filter((e) => !e.desktopOnly) }))
			.filter((g) => g.entries.length > 0)
	);
	const bottom = [
		{ id: 'messages', label: 'Messages', icon: 'messages' },
		{ id: 'map', label: 'Map', icon: 'map' },
		{ id: 'group-albums', label: 'Albums', icon: 'albums' }
	] as const;

	let dates = $state(false);
	let paused: Record<string, boolean> = $state({ 'Station 1 to my phone': false, 'Shed group chat': true });

	function modal(node: HTMLDialogElement) {
		node.showModal();
		const cancel = (e: Event) => {
			e.preventDefault();
			goto(with_({ more: null }));
		};
		node.addEventListener('cancel', cancel);
		return { destroy: () => node.removeEventListener('cancel', cancel) };
	}
</script>

<svelte:head><title>{open ? 'Message' : title(at)} · Prototype {look_.name} · PagerArchive</title></svelte:head>

<div class="p-shell" class:shrunk>
	<a class="p-skip" href="#p-main">Skip to content</a>
	<nav class="p-side" aria-label="Main menu">
		<Menu {admin} {shrunk} current={at} {go} toggle={with_({ menu: shrunk ? null : 'shrunk' })} />
	</nav>

	<header class="p-top">
		<nav class="p-crumbs" class:deep={where.length > 1} aria-label="Breadcrumbs">
			<ol>
				{#each where as c, i (i)}
					<li>
						{#if c.to && i < where.length - 1}<a href={go(c.to)}>{c.label}</a>
						{:else}<span aria-current={i === where.length - 1 ? 'page' : undefined}>{c.label}</span>{/if}
					</li>
				{/each}
			</ol>
		</nav>
		<!-- R33a: one search box, with the date range beside it. -->
		<form class="p-search" role="search" onsubmit={(e) => e.preventDefault()}>
			<label class="box">
				<Icon name="search" />
				<span class="visually-hidden">Search messages</span>
				<input type="search" placeholder={'Search: text, alias:"Station 1", priority:emergency'} />
			</label>
			<button class="p-btn ghost when" type="button" aria-expanded={dates} aria-controls="p-dates" aria-label="Date range" onclick={() => (dates = !dates)}
				><Icon name="calendar" /></button
			>
			<div id="p-dates" class="dates" class:shown={dates}>
				<label><span>From</span><input type="date" value="2026-10-30" /></label>
				<label><span>To</span><input type="date" value="2026-11-01" /></label>
			</div>
		</form>
		<details class="p-account">
			<summary class="p-btn ghost" aria-label="Account: Test User"><span class="avatar" aria-hidden="true">TU</span></summary>
			<ul>
				<li><a href={go('profile')}><Icon name="profile" />Profile</a></li>
				<li><a href={go('messages')}><Icon name="signout" />Sign out</a></li>
			</ul>
		</details>
	</header>

	<main id="p-main" class="p-content" class:has-open={at === 'messages' && open}>
		{#if at === 'messages'}
			<section class="p-list" aria-labelledby="p-list-title">
				<div class="head">
					<h1 id="p-list-title">Messages</h1>
					<span class="live"><span class="pulse" aria-hidden="true"></span>Live</span>
					<span class="n">{messages.length} messages</span>
					<div class="views" role="group" aria-label="View">
						<a class="p-btn secondary" href={go('messages')} aria-current="page"><Icon name="messages" />List</a>
						<a class="p-btn secondary" href={go('map')}><Icon name="map" />Map</a>
					</div>
				</div>
				<div class="rows">
					{#each messages as m (m.id)}
						<Row
							{m}
							{tz}
							selected={open?.id === m.id}
							open={with_({ m: String(m.id) })}
							alias={go(m.alias_id ? 'alias' : 'unknown')}
							job={go('job')}
						/>
					{/each}
				</div>
				<div class="foot"><button class="p-btn secondary" type="button">Load older messages</button></div>
			</section>
			{#if open}
				<section class="p-pane" aria-label="Message">
					{#key open.id}
						<Detail m={open} {tz} close={with_({ m: null })} alias={go(open.alias_id ? 'alias' : 'unknown')} job={go('job')} map={go('map')} />
					{/key}
				</section>
			{/if}
		{:else}
			<section class="p-placeholder" aria-labelledby="p-page-title">
				<h1 id="p-page-title">{title(at)}</h1>
				{#if findGroup(at)}
					{@const g = findGroup(at)!}
					<ul class="cards">
						{#each g.entries as e (e.id)}
							<li><a class="p-btn secondary" href={go(e.id)}><Icon name={e.icon} />{e.label}</a></li>
						{/each}
					</ul>
				{:else if at === 'templates'}
					<ul class="cards">
						<li><a class="p-btn secondary" href={go('template-fire')}><Icon name="templates" />Example Fire</a></li>
					</ul>
				{:else if at === 'integrations'}
					<!-- R81: on phones, only a Pause/Resume switch per integration. -->
					<ul class="switches">
						{#each Object.keys(paused) as name (name)}
							<li>
								<span><Icon name="integrations" />{name}</span>
								<button class="p-switch" type="button" role="switch" aria-checked={!paused[name]} aria-label="{name}: sending" onclick={() => (paused[name] = !paused[name])}
									><span class="knob" aria-hidden="true"></span><span class="state">{paused[name] ? 'Paused' : 'Sending'}</span></button
								>
							</li>
						{/each}
					</ul>
					<p class="desk"><button class="p-btn primary" type="button"><Icon name="integrations" />Set up an integration</button></p>
				{/if}
				<p class="note">
					This page isn't part of the prototype: it's here to show the menu{findEntry(at)?.entry.desktopOnly ? ' (it is left out of the phone menu, R81)' : ''} and the
					breadcrumbs (R79).
				</p>
			</section>
		{/if}
	</main>

	<!-- R81: phones get a bottom bar. -->
	<nav class="p-bottom" aria-label="Main menu, phone">
		<ul>
			{#each bottom as b (b.id)}
				<li>
					<a href={go(b.id)} aria-current={at === b.id && !more ? 'page' : undefined}><Icon name={b.icon} /><span>{b.label}</span></a>
				</li>
			{/each}
			<li>
				<a href={with_({ more: '1' })} aria-current={more ? 'page' : undefined}><Icon name="more" /><span>More</span></a>
			</li>
		</ul>
	</nav>

	{#if more}
		<dialog class="p-more" use:modal aria-labelledby="p-more-title">
			<div class="head">
				<h2 id="p-more-title">More</h2>
				<a class="p-btn ghost" href={with_({ more: null })} aria-label="Close"><Icon name="close" /></a>
			</div>
			{#each phoneGroups as g (g.id)}
				<h3><Icon name={g.icon} />{g.label}</h3>
				<ul>
					{#each g.entries as e (e.id)}
						<li><a href={go(e.id)}><Icon name={e.icon} />{e.label}</a></li>
					{/each}
				</ul>
			{/each}
		</dialog>
	{/if}

	{#if !bare}
		<details class="p-proto">
			<summary class="p-btn secondary">Prototype: {look_.name}</summary>
			<div class="panel">
				<p class="what">Three looks for one structure (ADR 0015). Example data only.</p>
				<ul class="looks">
					{#each looks as l (l.id)}
						<li>
							<a href={with_({ look: l.id === 'ledger' ? null : l.id })} aria-current={l.id === look ? 'true' : undefined}>{l.name}</a>
							<span>{l.aim[0]} {l.aim[1]}</span>
						</li>
					{/each}
				</ul>
				<p class="opts">
					Theme:
					<a href={with_({ theme: null })} aria-current={theme === 'auto' ? 'true' : undefined}>Auto</a>
					<a href={with_({ theme: 'light' })} aria-current={theme === 'light' ? 'true' : undefined}>Light</a>
					<a href={with_({ theme: 'dark' })} aria-current={theme === 'dark' ? 'true' : undefined}>Dark</a>
				</p>
				<p class="opts">
					Signed in as:
					<a href={with_({ role: null })} aria-current={admin ? 'true' : undefined}>an admin</a>
					<a href={with_({ role: 'user' })} aria-current={!admin ? 'true' : undefined}>a user</a>
				</p>
			</div>
		</details>
	{/if}
</div>
