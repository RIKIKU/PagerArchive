// The side menu's state: shrunk to group icons or not, and which groups the
// person opened or closed (R77). Kept in this browser, and only once they
// change something; until then each group is open except Administration,
// and the group holding the current page is always open.
const KEY = 'menu';

export const menu = $state({ shrunk: false, open: {} as Record<string, boolean> });

try {
	const saved = JSON.parse(localStorage.getItem(KEY) ?? 'null');
	if (saved && typeof saved === 'object') {
		menu.shrunk = saved.shrunk === true;
		for (const [k, v] of Object.entries(saved.open ?? {})) if (typeof v === 'boolean') menu.open[k] = v;
	}
} catch {
	// storage unavailable or unreadable: the defaults
}

function save() {
	try {
		localStorage.setItem(KEY, JSON.stringify({ shrunk: menu.shrunk, open: menu.open }));
	} catch {
		// storage unavailable: it lasts for this page
	}
}

export function toggleShrunk() {
	menu.shrunk = !menu.shrunk;
	save();
}

export function toggleGroup(id: string, open: boolean) {
	menu.open[id] = !open;
	save();
}

export const isOpen = (id: string, holdsCurrent: boolean) => holdsCurrent || (menu.open[id] ?? id !== 'admin');
