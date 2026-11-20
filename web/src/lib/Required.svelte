<script lang="ts">
	// R82g: ArchiveUx's Required tag in a label (0.2.1, the owner's "B"), and,
	// for a field that's required only sometimes, when, in words after it.
	// The name reads "Name required" (or "… required none saved yet").
	let { when }: { when?: string } = $props();
</script>

<span class="required">required</span>{#if when}{' '}<span class="required-when">{when}</span>{/if}
