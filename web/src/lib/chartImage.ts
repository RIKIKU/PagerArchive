// R38a: a report's chart as a PNG image, to download or copy. A chart is an
// SVG drawn with the tokens (R82c) through CSS classes, which a standalone
// image doesn't have: each element's computed style is written onto a copy,
// so the image looks as the page does, in the look and theme it's in. A
// table drawn as a chart (the heat grid) is painted cell by cell.

/** A size token in pixels: px, or rem times the root's font size. */
export function px(name: string): number {
	const root = getComputedStyle(document.documentElement);
	const v = root.getPropertyValue(name).trim();
	return v.endsWith('rem') ? parseFloat(v) * parseFloat(root.fontSize) : parseFloat(v);
}

const styled = ['fill', 'stroke', 'stroke-width', 'opacity', 'font-size', 'font-family', 'font-weight', 'text-anchor', 'dominant-baseline'];

function scale() {
	return Math.max(2, Math.ceil(window.devicePixelRatio || 1));
}

function background(el: Element): string {
	for (let e: Element | null = el; e; e = e.parentElement) {
		const bg = getComputedStyle(e).backgroundColor;
		if (bg && bg !== 'transparent' && !/rgba\(0, 0, 0, 0\)/.test(bg)) return bg;
	}
	return getComputedStyle(document.body).backgroundColor;
}

function toBlob(canvas: HTMLCanvasElement): Promise<Blob> {
	return new Promise((resolve, reject) => canvas.toBlob((b) => (b ? resolve(b) : reject(new Error("the image couldn't be made"))), 'image/png'));
}

/** The SVG as a PNG, at least twice its size on screen. */
async function svgImage(svg: SVGSVGElement): Promise<Blob> {
	const { width, height } = svg.getBoundingClientRect();
	const copy = svg.cloneNode(true) as SVGSVGElement;
	const from = [svg, ...svg.querySelectorAll('*')];
	const to = [copy, ...copy.querySelectorAll('*')];
	from.forEach((el, i) => {
		const cs = getComputedStyle(el);
		const style = styled.map((p) => `${p}:${cs.getPropertyValue(p)}`).join(';');
		to[i].setAttribute('style', style);
		to[i].removeAttribute('class');
	});
	copy.setAttribute('width', String(width));
	copy.setAttribute('height', String(height));
	// A data: URL, which the app's Content Security Policy allows for images
	// (img-src 'self' data:), where a blob: URL is refused.
	const url = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(new XMLSerializer().serializeToString(copy))}`;
	const img = new Image();
	await new Promise((resolve, reject) => {
		img.onload = resolve;
		img.onerror = () => reject(new Error("the chart couldn't be drawn"));
		img.src = url;
	});
	const k = scale();
	const canvas = document.createElement('canvas');
	canvas.width = Math.ceil(width * k);
	canvas.height = Math.ceil(height * k);
	const g = canvas.getContext('2d')!;
	g.fillStyle = background(svg);
	g.fillRect(0, 0, canvas.width, canvas.height);
	g.drawImage(img, 0, 0, canvas.width, canvas.height);
	return toBlob(canvas);
}

/** A table as a PNG: each cell's background, border and text as shown. */
async function tableImage(table: HTMLTableElement): Promise<Blob> {
	const box = table.getBoundingClientRect();
	const k = scale();
	const canvas = document.createElement('canvas');
	canvas.width = Math.ceil(box.width * k);
	canvas.height = Math.ceil(box.height * k);
	const g = canvas.getContext('2d')!;
	g.scale(k, k);
	g.fillStyle = background(table);
	g.fillRect(0, 0, box.width, box.height);
	for (const cell of table.querySelectorAll<HTMLElement>('th, td, caption')) {
		const r = cell.getBoundingClientRect();
		const cs = getComputedStyle(cell);
		const x = r.left - box.left;
		const y = r.top - box.top;
		if (cs.backgroundColor && !/rgba\(0, 0, 0, 0\)/.test(cs.backgroundColor)) {
			g.fillStyle = cs.backgroundColor;
			g.fillRect(x, y, r.width, r.height);
		}
		const bottom = parseFloat(cs.borderBottomWidth);
		if (bottom > 0) {
			g.fillStyle = cs.borderBottomColor;
			g.fillRect(x, y + r.height - bottom, r.width, bottom);
		}
		const text = (cell.innerText || '').trim();
		if (!text) continue;
		g.fillStyle = cs.color;
		g.font = `${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
		g.textBaseline = 'middle';
		const align = cs.textAlign === 'right' || cs.textAlign === 'end' ? 'right' : cs.textAlign === 'center' ? 'center' : 'left';
		g.textAlign = align;
		const pad = parseFloat(cs.paddingLeft);
		const tx = align === 'right' ? x + r.width - parseFloat(cs.paddingRight) : align === 'center' ? x + r.width / 2 : x + pad;
		g.fillText(text, tx, y + r.height / 2, r.width);
	}
	return toBlob(canvas);
}

/** The chart in an element (an SVG, or a table drawn as one) as a PNG. */
export function chartImage(el: Element): Promise<Blob> {
	if (el instanceof SVGSVGElement) return svgImage(el);
	if (el instanceof HTMLTableElement) return tableImage(el);
	const svg = el.querySelector('svg.chart');
	if (svg instanceof SVGSVGElement) return svgImage(svg);
	const table = el.querySelector('table');
	if (table) return tableImage(table);
	return Promise.reject(new Error('no chart to save'));
}

/** Whether this browser can put an image on the clipboard. */
export function canCopyImage(): boolean {
	return typeof ClipboardItem !== 'undefined' && !!navigator.clipboard?.write;
}

/** Puts the chart on the clipboard as a PNG. */
export async function copyImage(el: Element): Promise<void> {
	await navigator.clipboard.write([new ClipboardItem({ 'image/png': chartImage(el) })]);
}
