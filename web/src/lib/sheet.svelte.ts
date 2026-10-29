// R67: the print sheet. The signed-in user's ticked messages, in the order
// they were added, kept in this browser's localStorage under a key with the
// user's ID, so people sharing a browser each have their own. Only message
// IDs are stored, never text or any other field (A60). Storage that can't be
// read or written leaves the sheet working, empty, for this page's life.

/** How many messages a sheet holds; the API's limit too (ADR 0012). */
export const MAX_SHEET = 100;

export const sheet = $state({
	ids: [] as number[],
	/** False until the signed-in user is known. */
	ready: false,
	/** Said once when a tick is refused because the sheet is full. */
	notice: ''
});

let key: string | undefined;

const storageKey = (userId: number) => `pagerarchive:print-sheet:${userId}`;

// Only positive whole numbers, each once, at most MAX_SHEET: anything else
// in storage is dropped.
function clean(v: unknown): number[] {
	if (!Array.isArray(v)) return [];
	const out: number[] = [];
	for (const id of v) {
		if (Number.isSafeInteger(id) && id > 0 && !out.includes(id)) out.push(id);
	}
	return out.slice(0, MAX_SHEET);
}

function read(): number[] {
	if (!key) return [];
	try {
		const raw = localStorage.getItem(key);
		return raw ? clean(JSON.parse(raw)) : [];
	} catch {
		return [];
	}
}

function write(ids: number[]) {
	sheet.ids = ids;
	if (!key) return;
	try {
		if (ids.length) localStorage.setItem(key, JSON.stringify(ids));
		else localStorage.removeItem(key);
	} catch {
		// storage unavailable or full: the sheet lasts as long as this page
	}
}

/** Loads userId's sheet, and follows changes made in other tabs. */
export function loadSheet(userId: number): () => void {
	key = storageKey(userId);
	sheet.ids = read();
	sheet.ready = true;
	const follow = (e: StorageEvent) => {
		// key null: the whole of storage was cleared.
		if (e.key === key || e.key === null) sheet.ids = read();
	};
	window.addEventListener('storage', follow);
	return () => window.removeEventListener('storage', follow);
}

export const onSheet = (id: number) => sheet.ids.includes(id);

/** Adds or removes id; false if the sheet was full and id wasn't added. */
export function tick(id: number, on: boolean): boolean {
	if (!sheet.ready) return false;
	sheet.notice = '';
	if (!on) {
		write(sheet.ids.filter((x) => x !== id));
		return true;
	}
	if (onSheet(id)) return true;
	if (sheet.ids.length >= MAX_SHEET) {
		sheet.notice = `The print sheet is full: it holds ${MAX_SHEET} messages. Remove some before adding more.`;
		return false;
	}
	write([...sheet.ids, id]);
	return true;
}

/** Removes messages that no longer exist. */
export function drop(gone: number[]) {
	if (gone.length) write(sheet.ids.filter((x) => !gone.includes(x)));
}

export function clearSheet() {
	sheet.notice = '';
	write([]);
}
