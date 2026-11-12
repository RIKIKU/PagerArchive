// The scrubber on the page, if there is one (R81b), so the frame's skip link
// can take the keyboard to it. Set by Scrubber.svelte while it's shown.
export const scrubberHere: { focus?: () => void } = $state({});
