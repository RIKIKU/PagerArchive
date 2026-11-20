/** A run of a copy's text, and whether it differs from the message's. */
export interface Part {
	text: string;
	changed: boolean;
}

/** The longest text compared character by character: beyond it (4,096 by
 * 4,096 would be 16 million steps), only the differing middle is marked. */
const MAX_CELLS = 250_000;

/**
 * R19a: a near duplicate's text in runs, each marked if it isn't in the
 * message's (by the longest common subsequence of their characters), so the
 * detail view can show where the receivers disagreed.
 */
export function textDiff(message: string, copy: string): Part[] {
	const a = [...message];
	const b = [...copy];
	let pre = 0;
	while (pre < a.length && pre < b.length && a[pre] === b[pre]) pre++;
	let suf = 0;
	while (suf < a.length - pre && suf < b.length - pre && a[a.length - 1 - suf] === b[b.length - 1 - suf]) suf++;
	const am = a.slice(pre, a.length - suf);
	const bm = b.slice(pre, b.length - suf);
	const changed: boolean[] = new Array(bm.length).fill(true);
	if (am.length > 0 && bm.length > 0 && am.length * bm.length <= MAX_CELLS) {
		// lcs[i][j]: the longest common subsequence of am[i:] and bm[j:].
		const w = bm.length + 1;
		const lcs = new Uint16Array((am.length + 1) * w);
		for (let i = am.length - 1; i >= 0; i--)
			for (let j = bm.length - 1; j >= 0; j--)
				lcs[i * w + j] = am[i] === bm[j] ? lcs[(i + 1) * w + j + 1] + 1 : Math.max(lcs[(i + 1) * w + j], lcs[i * w + j + 1]);
		for (let i = 0, j = 0; i < am.length && j < bm.length; ) {
			if (am[i] === bm[j]) {
				changed[j] = false;
				i++;
				j++;
			} else if (lcs[(i + 1) * w + j] >= lcs[i * w + j + 1]) i++;
			else j++;
		}
	}
	const parts: Part[] = [];
	const push = (text: string, c: boolean) => {
		if (!text) return;
		const last = parts[parts.length - 1];
		if (last && last.changed === c) last.text += text;
		else parts.push({ text, changed: c });
	};
	push(b.slice(0, pre).join(''), false);
	bm.forEach((ch, i) => push(ch, changed[i]));
	push(b.slice(b.length - suf).join(''), false);
	return parts;
}
