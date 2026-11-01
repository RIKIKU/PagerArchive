<script lang="ts">
	// The Account group's start page. Before M13 this address was the profile
	// with the invitations on it: a link to them (#invite) goes to their page.
	import { goto } from '$app/navigation';
	import GroupPage from '#lib/GroupPage.svelte';
	import { groups } from '#lib/nav.ts';

	const invite = location.hash === '#invite';
	if (invite) goto('/account/invite', { replaceState: true });
</script>

{#if !invite}<GroupPage group={groups.find((g) => g.id === 'account')!} />{/if}
