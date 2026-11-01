// Desktop or phone is decided by screen width, never by the device (R81):
// phones are under 768 px (48rem), and from 1,280 px (80rem) a message opens
// in a pane beside the list (R80). The same widths are in the layout's
// @media rules, which can't use tokens.
const query = (q: string) => (typeof window === 'undefined' ? undefined : window.matchMedia(q));

export const screen = $state({ phone: false, wide: false });

const phone = query('(max-width: 47.99rem)');
const wide = query('(min-width: 80rem)');
if (phone && wide) {
	screen.phone = phone.matches;
	screen.wide = wide.matches;
	phone.addEventListener('change', (e) => (screen.phone = e.matches));
	wide.addEventListener('change', (e) => (screen.wide = e.matches));
}
