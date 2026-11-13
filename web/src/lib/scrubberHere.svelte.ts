// The scrubber on the page, if there is one (R81b), so the frame's skip link
// can take the keyboard to it. Set by Scrubber.svelte while it's shown.
export const scrubberHere: { focus?: () => void } = $state({});

// The scrubber is the right edge's control, with a mouse or a finger: the
// page's own scroll bar is hidden while one is on the page (owner,
// 2026-11-13), and the room it took is kept as padding, so nothing moves
// sideways between pages with a scrubber and pages without (app.css).
// Wheels, trackpads, fingers and keys still scroll. Counted, so a scrubber
// that comes before the last one has gone doesn't lose it.
let owners = 0;
export function ownEdge(): () => void {
	const html = document.documentElement;
	if (owners++ === 0) {
		// Measured by the page itself: a browser that hides its scroll bars but
		// keeps their room (a headless one) doesn't count it in clientWidth.
		const before = document.body.getBoundingClientRect().right;
		html.style.setProperty('--scrollbar-gap', '0px');
		html.classList.add('scrubber-page');
		const room = `${Math.max(0, Math.round(document.body.getBoundingClientRect().right - before))}px`;
		html.style.setProperty('--scrollbar-gap', room);
	}
	return () => {
		if (--owners === 0) html.classList.remove('scrubber-page');
	};
}
