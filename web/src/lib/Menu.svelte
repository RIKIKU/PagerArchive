<script lang="ts">
	// R77: the side menu. Each page has an icon and a label, under group
	// headings that open and close; each heading links to its group's start
	// page. Shrunk, only the group icons show (Messages has its own), each
	// with a tooltip and an accessible name, and hovering over one or
	// focusing it opens a panel of its pages. R44b, R55: Administration
	// carries the count of alias requests waiting; R67: the print sheet its
	// count of messages; R39: admins see when a receiver has gone quiet.
	import { albums } from './albums.svelte.ts';
	import Icon from './Icon.svelte';
	import { entryOf, messages, visible, type Entry, type Group } from './nav.ts';
	import { isOpen, menu, toggleGroup, toggleShrunk } from './menu.svelte.ts';

	let {
		path,
		admin,
		listHref,
		waiting,
		sheetCount,
		quiet
	}: { path: string; admin: boolean; listHref: string; waiting: number; sheetCount: number; quiet: number } = $props();

	const shrunk = $derived(menu.shrunk);
	const current = $derived(entryOf(path));
	const holds = (g: Group) => path === g.href || current?.group?.id === g.id || (g.id === 'albums' && path.startsWith('/albums/'));
	const isCurrent = (e: Entry) => current?.entry === e;
	const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
	const waitingName = (n: number) => (n > 0 ? `, ${plural(n, 'alias request', 'alias requests')} waiting` : '');
</script>

<div class="menu" class:shrunk>
	<a class="brand" href={listHref} aria-label={shrunk ? 'PagerArchive' : undefined}
		><span class="mark" aria-hidden="true">PA</span><span class="name">PagerArchive</span></a
	>
	<ul class="tree">
		<li class="group">
			<a class="item top" href={listHref} aria-current={path === '/' ? 'page' : undefined} aria-label={shrunk ? messages.label : undefined}
				><Icon name={messages.icon} /><span class="label">{messages.label}</span></a
			>
			{#if shrunk}<span class="tip" aria-hidden="true">{messages.text}</span>{/if}
		</li>
		{#each visible(admin) as g (g.id)}
			{@const open = shrunk || isOpen(g.id, holds(g))}
			{@const count = g.id === 'admin' ? waiting : 0}
			<li class="group">
				<div class="heading">
					<a
						class="item head"
						href={g.href}
						aria-current={path === g.href ? 'page' : undefined}
						aria-label={shrunk || count ? `${g.label}${waitingName(count)}` : undefined}
						><Icon name={g.icon} /><span class="label">{g.label}</span>{#if count}<span class="count" aria-hidden="true"
								>{count}</span
							>{/if}</a
					>
					{#if !shrunk}
						<button
							class="toggle"
							type="button"
							aria-expanded={open}
							aria-controls="menu-{g.id}"
							aria-label="{g.label}: {open ? 'hide' : 'show'} its pages"
							onclick={() => toggleGroup(g.id, open)}><Icon name={open ? 'open' : 'closed'} /></button
						>
					{/if}
				</div>
				<!-- Shrunk, the panel opens with what's in the group, in a few
				     words: its tooltip, which doesn't just repeat its name. -->
				<ul id="menu-{g.id}" class="entries" hidden={!open}>
					{#if shrunk}<li class="flyhead" aria-hidden="true">{g.text}</li>{/if}
					{#if g.id === 'albums'}
						<!-- R77: one entry per album, with how many messages it holds. -->
						{#each albums.list as a (a.id)}
							<li>
								<a
									class="item"
									href="/albums/{a.id}"
									aria-current={path === `/albums/${a.id}` ? 'page' : undefined}
									aria-label="{a.name}, {plural(a.messages, 'message', 'messages')}"
									><Icon name="albums" /><span class="label">{a.name}</span><span class="count quiet" aria-hidden="true">{a.messages}</span></a
								>
							</li>
						{/each}
					{/if}
					{#each g.entries as e (e.href)}
						<li>
							{#if e.href === '/sheet'}
								<a
									class="item"
									href={e.href}
									aria-current={isCurrent(e) ? 'page' : undefined}
									data-testid="sheet-link"
									aria-label="{e.label}, {plural(sheetCount, 'message', 'messages')}"
									><Icon name={e.icon} /><span class="label">{e.label}</span><span class="count quiet" aria-hidden="true">{sheetCount}</span></a
								>
							{:else if e.href === '/admin/requests' && waiting > 0}
								<a class="item" href={e.href} aria-current={isCurrent(e) ? 'page' : undefined} aria-label="{e.label}{waitingName(waiting)}"
									><Icon name={e.icon} /><span class="label">{e.label}</span><span class="count" aria-hidden="true">{waiting}</span></a
								>
							{:else}
								<a class="item" href={e.href} aria-current={isCurrent(e) ? 'page' : undefined}
									><Icon name={e.icon} /><span class="label">{e.label}</span></a
								>
							{/if}
						</li>
					{/each}
				</ul>
			</li>
		{/each}
	</ul>
	<div class="foot">
		{#if admin && quiet > 0}
			<div class="tipped">
				<a class="item alert" href="/admin/clients" aria-label={shrunk ? `${plural(quiet, 'receiver', 'receivers')} not receiving` : undefined}
					><Icon name="warning" /><span class="label" data-testid="not-receiving">{plural(quiet, 'receiver', 'receivers')} not receiving</span></a
				>
				{#if shrunk}<span class="tip" aria-hidden="true">No pages from {quiet === 1 ? 'a receiver' : 'some receivers'} for 30 minutes</span>{/if}
			</div>
		{/if}
		<div class="tipped">
			<button class="item shrink" type="button" aria-label={shrunk ? 'Widen the menu' : undefined} aria-pressed={shrunk} onclick={toggleShrunk}
				><Icon name={shrunk ? 'widen' : 'shrink'} /><span class="label">Shrink menu</span></button
			>
			{#if shrunk}<span class="tip" aria-hidden="true">Show the pages' names again</span>{/if}
		</div>
	</div>
</div>

<style>
	.menu {
		display: flex;
		flex-direction: column;
		gap: var(--space-2xs);
		min-height: 100%;
		padding: var(--space-md) var(--space-sm);
	}
	.brand {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-2xs) var(--space-sm) var(--space-md);
		color: var(--fg);
		font-size: var(--text-lg);
		font-weight: var(--weight-strong);
		text-decoration: none;
	}
	.mark {
		display: inline-grid;
		place-items: center;
		flex: none;
		width: var(--avatar-size);
		height: var(--avatar-size);
		border-radius: var(--radius-button);
		background: var(--accent);
		color: var(--accent-fg);
		font-size: var(--text-xs);
		font-weight: var(--weight-strong);
	}
	.tree,
	.entries {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.tree {
		display: flex;
		flex-direction: column;
		gap: var(--space-3xs);
		flex: 1;
	}
	.heading {
		display: flex;
		align-items: center;
	}
	.item {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		width: 100%;
		min-height: var(--target);
		padding: 0 var(--space-sm);
		border: 0;
		border-radius: var(--radius-button);
		background: transparent;
		color: var(--fg);
		font-family: inherit;
		font-size: var(--text-md);
		font-weight: var(--weight-normal);
		line-height: var(--leading-tight);
		text-align: left;
		text-decoration: none;
		cursor: pointer;
	}
	.item:hover {
		background: var(--hover);
	}
	.item:active,
	.item[aria-current='page'] {
		background: var(--accent-soft);
		color: var(--accent-soft-fg);
	}
	.item[aria-current='page'],
	.head {
		font-weight: var(--weight-strong);
	}
	.entries .item {
		padding-left: var(--space-2xl);
	}
	.label {
		flex: 1;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.toggle {
		flex: none;
		min-width: var(--target);
		padding: 0;
		border-color: transparent;
		background: transparent;
		color: var(--muted);
	}
	.toggle:hover {
		background: var(--hover);
		border-color: transparent;
	}
	.toggle:active {
		background: var(--accent-soft);
		border-color: transparent;
		color: var(--accent-soft-fg);
	}
	.count {
		margin-left: auto;
		min-width: var(--count-min);
		padding: 0 var(--space-xs);
		border-radius: var(--radius-pill);
		background: var(--accent);
		color: var(--accent-fg);
		font-size: var(--text-xxs);
		font-weight: var(--weight-strong);
		line-height: var(--leading);
		text-align: center;
	}
	.count.quiet {
		background: var(--accent-soft);
		color: var(--accent-soft-fg);
	}
	.foot {
		position: relative;
		display: flex;
		flex-direction: column;
		gap: var(--space-2xs);
		margin-top: var(--space-lg);
	}
	.alert {
		background: var(--warn-bg);
		color: var(--warn-fg);
		font-weight: var(--weight-strong);
	}
	.alert:hover {
		background: var(--warn-bg);
		text-decoration: underline;
	}
	.shrink {
		color: var(--muted);
	}

	/* Shrunk: the group icons only; hovering over one or focusing it opens
	   its panel. */
	.shrunk {
		align-items: center;
		padding: var(--space-md) var(--space-2xs);
	}
	.shrunk .brand .name,
	.shrunk .label {
		display: none;
	}
	.shrunk .brand {
		padding: var(--space-2xs) 0 var(--space-md);
	}
	.shrunk .tree,
	.shrunk .foot {
		width: 100%;
		align-items: center;
	}
	.shrunk .group,
	.shrunk .tipped {
		position: relative;
		display: flex;
		justify-content: center;
	}
	.shrunk .top,
	.shrunk .head,
	.shrunk .shrink,
	.shrunk .alert {
		position: relative;
		justify-content: center;
		width: var(--target-coarse);
		height: var(--target-coarse);
		padding: 0;
	}
	.shrunk .head .count {
		position: absolute;
		top: 0;
		right: 0;
		padding: 0 var(--space-3xs);
	}
	.shrunk .entries,
	.tip {
		display: none;
		position: absolute;
		left: 100%;
		top: 0;
		z-index: var(--layer-overlay);
	}
	.shrunk .entries {
		min-width: var(--flyout-width);
		padding: var(--space-xs);
		background: var(--surface-2);
		border: var(--border-width) solid var(--control-border);
		border-radius: var(--radius);
	}
	.shrunk .entries .item {
		padding-left: var(--space-sm);
	}
	.shrunk .entries .label {
		display: block;
	}
	.shrunk .group:hover > .entries,
	.shrunk .group:focus-within > .entries,
	.shrunk .group:hover > .tip,
	.shrunk .group:focus-within > .tip,
	.shrunk .tipped:hover > .tip,
	.shrunk .tipped:focus-within > .tip {
		display: block;
	}
	.flyhead {
		padding: var(--space-xs) var(--space-sm);
		color: var(--muted);
		font-size: var(--text-xs);
		font-weight: var(--weight-strong);
		letter-spacing: var(--badge-tracking);
		text-transform: var(--badge-case);
	}
	.tip {
		top: 50%;
		transform: translateY(-50%);
		padding: var(--space-2xs) var(--space-sm);
		border-radius: var(--radius-small);
		background: var(--fg);
		color: var(--bg);
		font-size: var(--text-xs);
		white-space: nowrap;
		pointer-events: none;
	}
</style>
