<script lang="ts">
	// R77a: an icon never stands alone, so it's hidden from assistive
	// technology; the label or tooltip beside it names it. Drawn by
	// ArchiveUx's Icon (R77d), with PagerArchive's own set (icons.ts) passed
	// as its markup: icons are compiled into the app, so nothing is fetched
	// from anywhere.
	import { Icon as UxIcon } from 'archiveux/components';
	import { icons, type IconName } from './icons.ts';

	let { name }: { name: IconName } = $props();
</script>

<UxIcon svg={icons[name]} />
