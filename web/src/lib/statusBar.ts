// R90 (ADR 0037): an installed app's title bar and status bar, and a phone
// browser's toolbar, take the colour of the app's top bar (--surface) in the
// look and theme it's shown in. app.html's theme-color lines give Harbour's
// before the app starts (scripts/pwa.mjs writes them); this keeps them right
// when the look, the chosen theme or the system's theme changes.

/** The top bar's colour as the browser draws it (tokens are light-dark() pairs, which only a computed colour resolves). */
function topBar(): string {
	const probe = document.createElement('span');
	probe.style.color = 'var(--surface)';
	probe.hidden = true;
	document.documentElement.append(probe);
	const colour = getComputedStyle(probe).color;
	probe.remove();
	return colour;
}

/** Keeps the theme-color lines on the top bar's colour; returns a function that stops. */
export function followTopBar(): () => void {
	const update = () => {
		const colour = topBar();
		for (const m of document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]')) {
			if (m.content !== colour) m.content = colour;
		}
	};
	update();
	const observer = new MutationObserver(update);
	observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme', 'data-look'] });
	const system = matchMedia('(prefers-color-scheme: dark)');
	system.addEventListener('change', update);
	return () => {
		observer.disconnect();
		system.removeEventListener('change', update);
	};
}
