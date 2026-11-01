<script lang="ts">
	import GroupPage from '#lib/GroupPage.svelte';
	import { groups } from '#lib/nav.ts';
</script>

<GroupPage group={groups.find((g) => g.id === 'reports')!} />
