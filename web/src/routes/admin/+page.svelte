<script lang="ts">
	// R44b: the Administration start page links to every admin screen.
	import { api, type ClientStatus } from '#lib/api.ts';
	import { refreshWaiting, waiting } from '#lib/waiting.svelte.ts';

	let quiet = $state(0);
	api
		.clients()
		.then((cs: ClientStatus[]) => (quiet = cs.filter((c) => !c.receiving).length))
		.catch(() => {});
	refreshWaiting();

	const screens = [
		{ href: '/admin/users', title: 'Users', text: 'Users and their integrations, roles, deactivation and unused invitations.' },
		{ href: '/admin/aliases', title: 'Aliases', text: 'Add, edit and delete aliases, see their history, and export them as a CSV.' },
		{ href: '/admin/templates', title: 'Templates', text: 'Add, edit and delete templates: agency, icon, colour and agency profile.' },
		{ href: '/admin/requests', title: 'Alias requests', text: 'Requests from users for new aliases, changes and deletions, to approve or reject.' },
		{ href: '/admin/clients', title: 'Receiving clients', text: 'Which receivers are sending pages, and which have gone quiet.' },
		{ href: '/admin/retention', title: 'Retention', text: 'How long messages are kept, and the nightly clean-up.' },
		{ href: '/admin/integration-hosts', title: 'Integration addresses', text: 'Private hosts and ranges that integrations may reach.' }
	];
</script>

<svelte:head><title>Administration · PagerArchive</title></svelte:head>

<h1>Administration</h1>
<ul class="screens" data-testid="admin-screens">
	{#each screens as s (s.href)}
		<li>
			<a href={s.href}>{s.title}</a>
			{#if s.href === '/admin/clients' && quiet > 0}<span class="tag warn">{quiet} not receiving</span>{/if}
			{#if s.href === '/admin/requests' && waiting.count > 0}<span class="tag warn" data-testid="waiting-tag">{waiting.count} waiting</span>{/if}
			<p>{s.text}</p>
		</li>
	{/each}
</ul>

<style>
	.screens {
		list-style: none;
		padding: 0;
		margin: 0 0.75rem;
		max-width: 44rem;
	}
	.screens li {
		padding: 0.6rem 0;
		border-bottom: 1px solid var(--border);
	}
	.screens a {
		font-weight: 600;
		font-size: var(--text-lg);
	}
	.screens p {
		margin: 0.2rem 0 0;
		color: var(--muted);
	}
	.tag {
		font-size: var(--text-xs);
		border-radius: 1rem;
		padding: 0 0.5rem;
		margin-left: 0.5rem;
	}
	.tag.warn {
		background: var(--warn-bg);
		color: var(--warn-fg);
	}
</style>
