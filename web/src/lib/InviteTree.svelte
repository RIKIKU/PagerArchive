<script lang="ts" module>
	import type { TreeNode } from '#lib/api.ts';

	/** What a preview adds to the tree: the ticks, and who's new or changed
	 * since the last preview (R42b, ADR 0008 §5). */
	export interface Ticks {
		ticked: Set<number>;
		marks: Map<number, 'new' | 'changed'>;
		toggle: (id: number) => void;
		spare: (n: TreeNode) => void;
	}

	/** Everyone in a tree, the root first. */
	export function everyone(n: TreeNode): TreeNode[] {
		return [n, ...n.children.flatMap(everyone)];
	}
</script>

<script lang="ts">
	// R42a: an invite tree as an indented list. Deactivated people are
	// greyed out and the tree continues through them; a deleted account is
	// "deleted user". With ticks, it's a deactivation preview (R42b).
	import type { PersonRef } from '#lib/api.ts';
	import { date, isoAttr } from '#lib/format.ts';

	let {
		root,
		inviter = null,
		tz,
		ticks
	}: { root: TreeNode; inviter?: PersonRef | null; tz: string; ticks?: Ticks } = $props();

	const why: Record<string, string> = {
		'last admin': "Can't be ticked: the last active admin",
		you: "Can't be ticked: that's you",
		'already deactivated': 'Already deactivated',
		deleted: 'Deleted their account'
	};
	const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
</script>

{#snippet person(n: TreeNode)}
	<li class="person" class:off={n.status !== 'active'} data-testid="tree-person" data-person={n.label}>
		<div class="row">
			{#if ticks}
				<input
					id="tick-{n.id}"
					type="checkbox"
					checked={ticks.ticked.has(n.id)}
					disabled={!n.tick?.can}
					onchange={() => ticks.toggle(n.id)}
					aria-describedby="about-{n.id}"
				/>
				<label class="who" for="tick-{n.id}">{n.label}</label>
			{:else if n.status === 'deleted'}
				<span class="who">{n.label}</span>
			{:else}
				<a class="who" href="/admin/users/{n.id}">{n.label}</a>
			{/if}
			{#if !ticks && n.id === root.id}<span class="tag">This user</span>{/if}
			{#if n.role === 'admin' && n.status !== 'deleted'}<span class="tag">Admin</span>{/if}
			{#if n.trusted}<span class="tag">Trusted</span>{/if}
			{#if n.status === 'deactivated'}<span class="tag warn">Deactivated</span>{/if}
			{#if ticks?.marks.get(n.id) === 'new'}<span class="tag warn">New</span>{/if}
			{#if ticks?.marks.get(n.id) === 'changed'}<span class="tag warn">Changed</span>{/if}
		</div>
		<div class="meta" id="about-{n.id}">
			{#if n.status !== 'deleted'}{n.role === 'admin' ? 'Admin' : 'User'}{' · '}{/if}joined
			<time datetime={isoAttr(n.joined)}>{date(n.joined, tz)}</time>
			{#if n.invitations > 0}· {plural(n.invitations, 'unused invitation', 'unused invitations')}{/if}
			{#if ticks && n.tick?.why}· {why[n.tick.why]}{/if}
			{#if ticks && n.tick?.can && n.trusted && !ticks.ticked.has(n.id)}· Trusted, so not ticked to start with{/if}
		</div>
		{#if ticks && n.children.length > 0 && everyone(n).some((p) => p.tick?.can)}
			<button type="button" class="secondary spare" onclick={() => ticks.spare(n)} aria-label="Spare this branch: {n.label}"
				>Spare this branch</button
			>
		{/if}
		{#if n.children.length > 0}
			<ul>
				{#each n.children as c (c.id)}{@render person(c)}{/each}
			</ul>
		{/if}
	</li>
{/snippet}

<ul class="tree" data-testid="invite-tree">
	{#if inviter && !ticks}
		<li class="inviter">
			<div class="row">
				{#if inviter.deleted}<span class="who">{inviter.label}</span>{:else}<a class="who" href="/admin/users/{inviter.id}">{inviter.label}</a>{/if}
				<span class="tag">Invited them</span>
			</div>
			<ul>{@render person(root)}</ul>
		</li>
	{:else}
		{@render person(root)}
	{/if}
</ul>

<style>
	.tree {
		list-style: none;
		padding: 0;
		margin: 0 0.75rem;
	}
	.tree ul {
		list-style: none;
		margin: 0;
		padding-left: 1.1rem;
		border-left: 1px solid var(--border);
		margin-left: 0.4rem;
	}
	.person,
	.inviter {
		padding-top: 0.45rem;
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem 0.5rem;
		align-items: center;
	}
	.who {
		font-weight: 600;
		overflow-wrap: anywhere;
	}
	.off > .row .who {
		color: var(--muted);
		font-weight: 400;
	}
	.meta {
		color: var(--muted);
		font-size: var(--text-sm);
		overflow-wrap: anywhere;
	}
	.tag {
		font-size: var(--text-xs);
		border: 1px solid var(--border);
		border-radius: 1rem;
		padding: 0 0.5rem;
	}
	.tag.warn {
		background: var(--warn-bg);
		color: var(--warn-fg);
		border-color: var(--warn-fg);
	}
	.spare {
		margin-top: 0.3rem;
		font-size: var(--text-sm);
	}
</style>
