// Desktop or phone is decided by screen width, never by the device (R81):
// phones are under 768 px (48rem), and from 1,280 px (80rem) a message opens
// in a pane beside the list (R80). The same widths are in the layout's
// @media rules, which can't use tokens. touch: the main pointer is a finger
// (no hover, or a coarse pointer), whatever the width: the scrubber is then
// a grab tab (owner, 2026-11-13).
const query = (q: string) => (typeof window === 'undefined' ? undefined : window.matchMedia(q));

export const screen = $state({ phone: false, wide: false, touch: false });

const phone = query('(max-width: 47.99rem)');
const wide = query('(min-width: 80rem)');
const touch = query('(hover: none), (pointer: coarse)');
if (phone && wide && touch) {
	screen.phone = phone.matches;
	screen.wide = wide.matches;
	screen.touch = touch.matches;
	phone.addEventListener('change', (e) => (screen.phone = e.matches));
	wide.addEventListener('change', (e) => (screen.wide = e.matches));
	touch.addEventListener('change', (e) => (screen.touch = e.matches));
}
