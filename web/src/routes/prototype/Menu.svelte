<script lang="ts">
	import Icon from './Icon.svelte';
	import { groups, messagesEntry, type Group } from './looks.ts';

	// R77: the side menu. Open, each group opens and closes under its heading;
	// shrunk, only the group icons show (Messages has its own), and hovering
	// over one or focusing it opens a panel of its pages.
	let {
		admin,
		shrunk,
		current,
		go,
		toggle
	}: { admin: boolean; shrunk: boolean; current: string; go: (id: string) => string; toggle: string } = $props();

	let closed: Record<string, boolean> = $state({});
	const shown = $derived(groups.filter((g) => admin || !g.adminOnly));
	const holds = (g: Group) => current === `group-${g.id}` || g.entries.some((e) => e.id === current);
	const countLabel = (g: Group) => (g.count ? `, ${g.count} waiting` : '');
</script>

<div class="p-menu" class:shrunk>
	<a class="brand" href={go('messages')} aria-label={shrunk ? 'PagerArchive' : undefined}><span class="mark" aria-hidden="true">PA</span><span class="name">PagerArchive</span></a>
	<ul class="tree">
		<li class="group">
			<a class="item top" href={go('messages')} aria-current={current === 'messages' ? 'page' : undefined} aria-label={shrunk ? messagesEntry.label : undefined}
				><Icon name={messagesEntry.icon} /><span class="label">{messagesEntry.label}</span></a
			>
			{#if shrunk}<span class="tip" aria-hidden="true">{messagesEntry.label}</span>{/if}
		</li>
		{#each shown as g (g.id)}
			{@const open = shrunk || !closed[g.id]}
			<li class="group" class:holds={holds(g)}>
				{#if shrunk}
					<a class="item head" href={go(`group-${g.id}`)} aria-label="{g.label}{countLabel(g)}" aria-current={current === `group-${g.id}` ? 'page' : undefined}
						><Icon name={g.icon} />{#if g.count}<span class="count dot-count" aria-hidden="true">{g.count}</span>{/if}</a
					>
				{:else}
					<button
						class="item head"
						type="button"
						aria-expanded={open}
						aria-controls="pg-{g.id}"
						onclick={() => (closed[g.id] = !closed[g.id])}
						><Icon name={g.icon} /><span class="label">{g.label}</span>{#if g.count}<span class="count"
								>{g.count}<span class="visually-hidden"> waiting</span></span
							>{/if}<span class="chev"><Icon name={open ? 'open' : 'closed'} /></span></button
					>
				{/if}
				<ul id="pg-{g.id}" class="entries" hidden={!open}>
					{#if shrunk}<li class="flyhead" aria-hidden="true">{g.label}</li>{/if}
					{#each g.entries as e (e.id)}
						<li>
							<a class="item" href={go(e.id)} aria-current={current === e.id ? 'page' : undefined}
								><Icon name={e.icon} /><span class="label">{e.label}</span>{#if e.count}<span class="count"
										>{e.count}<span class="visually-hidden"> {e.id === 'print' ? 'messages' : 'waiting'}</span></span
									>{/if}</a
							>
						</li>
					{/each}
				</ul>
			</li>
		{/each}
	</ul>
	<a class="p-btn ghost shrink" href={toggle} aria-label={shrunk ? 'Widen the menu' : undefined}
		><Icon name={shrunk ? 'widen' : 'shrink'} /><span class="label">Shrink menu</span></a
	>
</div>
