// A73, R82c: every value that styles the UI is a token in src/app.css, and
// components use tokens only. This fails, naming each file, line and value,
// when a component hard-codes a colour, a length (a size, spacing, corner
// radius, border or target size), a type size, a font, a font weight, a
// line height or a z-index.
//
// It reads, with Svelte's own parser, every .svelte file under src:
//   - the <style> block: every declaration's value;
//   - inline styles: style="…" attributes and style:property directives,
//     their text and every string written in their expressions
//     (style="color: {'#f00'}", style:color={x ? '#f00' : y});
//     style:--agency={m.colour} takes its value from data, so it passes;
//   - class strings: a class attribute's text and strings, for a colour or
//     length written into a class name (a utility class such as "p-[4px]");
//   - attributes that style: a colour in fill, stroke, stop-color, color and
//     the other colour attributes; any value in stroke-width,
//     stroke-dasharray, font-size, font-weight, font-family, letter-spacing,
//     rx and ry; and width or height on an img or svg, unless from data.
// A border's or outline's width written as thin, medium or thick fails as a
// length does.
// And src/app.css, where hard-coded values are allowed only as the value of
// a token (a custom property, --name: value); its other rules use tokens
// too. Selectors and @media, @container and @supports conditions aren't
// values: a breakpoint can't be a token in CSS. Unitless numbers (flex: 1,
// opacity), percentages, fr, times and angles are layout, not look.
//
// usage: node scripts/check-tokens.mjs [file …]   (default: all of src)
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse } from 'svelte/compiler';

const web = fileURLToPath(new URL('..', import.meta.url));
const src = join(web, 'src');

// CSS named colours (CSS Color 4), as whole words.
const named =
	'aliceblue antiquewhite aqua aquamarine azure beige bisque black blanchedalmond blue blueviolet brown burlywood cadetblue chartreuse chocolate coral cornflowerblue cornsilk crimson cyan darkblue darkcyan darkgoldenrod darkgray darkgreen darkgrey darkkhaki darkmagenta darkolivegreen darkorange darkorchid darkred darksalmon darkseagreen darkslateblue darkslategray darkslategrey darkturquoise darkviolet deeppink deepskyblue dimgray dimgrey dodgerblue firebrick floralwhite forestgreen fuchsia gainsboro ghostwhite gold goldenrod gray green greenyellow grey honeydew hotpink indianred indigo ivory khaki lavender lavenderblush lawngreen lemonchiffon lightblue lightcoral lightcyan lightgoldenrodyellow lightgray lightgreen lightgrey lightpink lightsalmon lightseagreen lightskyblue lightslategray lightslategrey lightsteelblue lightyellow lime limegreen linen magenta maroon mediumaquamarine mediumblue mediumorchid mediumpurple mediumseagreen mediumslateblue mediumspringgreen mediumturquoise mediumvioletred midnightblue mintcream mistyrose moccasin navajowhite navy oldlace olive olivedrab orange orangered orchid palegoldenrod palegreen paleturquoise palevioletred papayawhip peachpuff peru pink plum powderblue purple rebeccapurple red rosybrown royalblue saddlebrown salmon sandybrown seagreen seashell sienna silver skyblue slateblue slategray slategrey snow springgreen steelblue tan teal thistle tomato turquoise violet wheat white whitesmoke yellow yellowgreen canvas canvastext linktext visitedtext activetext buttonface buttontext buttonborder field fieldtext highlight highlighttext selecteditem selecteditemtext mark marktext graytext accentcolor accentcolortext';
const namedColour = new RegExp(`(?<![\\w-])(${named.split(' ').join('|')})(?![\\w-])`, 'i');
const colourFunction = /(?<![\w-])(rgba?|hsla?|hwb|lab|lch|oklab|oklch|color|color-mix|light-dark)\(/i;
const hex = /(?<![\w&])#[0-9a-f]{3,8}(?![\w-])/i;
const units = 'px|rem|em|ex|ch|vw|vh|vmin|vmax|dvh|dvw|svh|svw|lvh|lvw|cm|mm|in|pt|pc|q|lh|rlh|cap|ic|cqw|cqh|cqi|cqb|cqmin|cqmax';
const length = new RegExp(`(?<![\\w.#-])-?(\\d*\\.?\\d+)(${units})(?![\\w-])`, 'gi');
const sizeKeyword = /(?<![\w-])(xx-small|x-small|small|medium|large|x-large|xx-large|xxx-large|smaller|larger)(?![\w-])/i;
const allowedWhole = /^(inherit|initial|unset|revert|revert-layer)$/i;

/** What's hard-coded in one declaration's value ([] when nothing). */
export function problems(property, value) {
	const prop = property.toLowerCase();
	// Token references are what's wanted; what's left is written in the code.
	const v = value
		.replace(/\/\*[\s\S]*?\*\//g, '')
		.replace(/!important/i, '')
		.trim();
	const rest = v.replace(/var\(--[\w-]+\)/g, '');
	const out = [];
	if (allowedWhole.test(v)) return out;
	const h = rest.match(hex);
	if (h) out.push(`colour ${h[0]}`);
	const f = rest.match(colourFunction);
	if (f) out.push(`colour ${f[1]}()`);
	const n = rest.match(namedColour);
	if (n && !/^(font-family|font|transition|transition-property|will-change|grid-template-areas|grid-area)$/.test(prop)) out.push(`colour ${n[1]}`);
	for (const m of rest.matchAll(length)) if (parseFloat(m[1]) !== 0) out.push(`length ${m[0]}`);
	if (prop === 'font-size' || prop === 'font') {
		const k = rest.match(sizeKeyword);
		if (k) out.push(`type size ${k[1]}`);
		if (/\d%/.test(rest)) out.push(`type size ${rest.match(/[\d.]+%/)[0]}`);
	}
	if (prop === 'font-family' && rest.replace(/[\s,]/g, '') !== '') out.push(`font ${v}`);
	if (prop === 'font-weight' && rest.trim() !== '') out.push(`font weight ${v}`);
	if (prop === 'line-height' && rest.trim() !== '' && rest.trim() !== 'normal') out.push(`line height ${v}`);
	if (prop === 'z-index' && /\d/.test(rest) && rest.trim() !== '0') out.push(`z-index ${v}`);
	// A border's or outline's width written as a keyword is a length too.
	if (/^(border|outline|column-rule)(-|$)/.test(prop)) {
		const k = rest.match(/(?<![\w-])(thin|medium|thick)(?![\w-])/i);
		if (k) out.push(`width ${k[1]}`);
	}
	return out;
}

function lineOf(text, offset) {
	return text.slice(0, offset).split('\n').length;
}

/** Declarations in a CSS AST block, with whether they're custom properties. */
function* declarations(node) {
	for (const child of node?.children ?? []) {
		if (child.type === 'Declaration') yield child;
		else if (child.type === 'Rule') yield* declarations(child.block);
		else if (child.type === 'Atrule' && child.block) yield* declarations(child.block);
	}
}

/** Style declarations written as text: "color: red; padding: 4px". */
function* inline(text) {
	for (const part of text.split(';')) {
		const i = part.indexOf(':');
		if (i > 0) yield [part.slice(0, i).trim(), part.slice(i + 1).trim()];
	}
}

function walk(node, visit) {
	if (!node || typeof node !== 'object') return;
	if (Array.isArray(node)) {
		for (const n of node) walk(n, visit);
		return;
	}
	if (node.type) visit(node);
	for (const [k, v] of Object.entries(node)) if (k !== 'parent' && v && typeof v === 'object') walk(v, visit);
}

/** The fixed values written in an expression: its strings, the text of its
 * template literals, and (numbers too) its numbers. */
function literals(expr, numbers = false) {
	const out = [];
	walk(expr, (n) => {
		if (n.type === 'Literal' && (typeof n.value === 'string' || (numbers && typeof n.value === 'number'))) out.push(String(n.value));
		else if (n.type === 'TemplateElement' && (n.value.cooked ?? n.value.raw)) out.push(n.value.cooked ?? n.value.raw);
	});
	return out;
}

// Attributes that draw in a colour (SVG's presentation attributes, and the
// old HTML ones), and those that set a stroke, type or a corner's radius.
const colourAttributes = new Set(['fill', 'stroke', 'stop-color', 'flood-color', 'lighting-color', 'color', 'bgcolor', 'background', 'text']);
const styleAttributes = new Set(['stroke-width', 'stroke-dasharray', 'font-size', 'font-weight', 'font-family', 'letter-spacing', 'rx', 'ry']);
// Values an attribute may hold without naming a colour or a size.
const keywordOnly = /^(none|currentcolor|transparent|inherit|auto|context-fill|context-stroke)$/i;

// Tokens defined in app.css, and those a component sets itself from data
// (style:--agency={…}): a var() naming anything else is a mistake.
const defined = new Set();
const uses = [];

function noteUses(name, line, value, where) {
	for (const m of value.matchAll(/var\(\s*(--[\w-]+)/g)) uses.push({ name, line, token: m[1], where });
}

function checkSvelte(file, text, report, name) {
	const ast = parse(text, { modern: true, filename: file });
	for (const d of declarations(ast.css)) {
		// A custom property set in a component is a hard-coded value too.
		for (const p of problems(d.property, d.value)) report(lineOf(text, d.start), `${d.property}: ${d.value.trim()}`, p);
		noteUses(name, lineOf(text, d.start), d.value, `${d.property}: ${d.value.trim()}`);
	}
	walk(ast.fragment, (n) => {
		if (!n.attributes) return;
		const element = (n.name ?? '').toLowerCase();
		for (const a of n.attributes) {
			if (a.type === 'StyleDirective' && a.name.startsWith('--')) defined.add(a.name);
			const parts = Array.isArray(a.value) ? a.value : a.value && a.value !== true ? [a.value] : [];
			// What the code writes: the text, and every string in an expression
			// ({x ? '#f00' : y}); what comes from data passes.
			const written = (p) => (p.type === 'Text' ? [p.data] : p.type === 'ExpressionTag' ? literals(p.expression) : []);
			const name = (a.name ?? '').toLowerCase();
			const line = lineOf(text, a.start);
			if (a.type === 'Attribute' && name === 'style') {
				// The attribute as one text, each expression standing in for a
				// placeholder, so a string inside braces is checked against the
				// property it's the value of: style="color: {'#f00'}".
				const holes = [];
				const joined = parts.map((p) => (p.type === 'Text' ? p.data : `\u0000${holes.push(p) - 1}\u0000`)).join('');
				for (const [prop, val] of inline(joined)) {
					const shown = val.replace(/\u0000\d+\u0000/g, '{…}');
					for (const q of problems(prop, val.replace(/\u0000\d+\u0000/g, ''))) report(line, `style="${prop}: ${shown}"`, q);
					for (const m of val.matchAll(/\u0000(\d+)\u0000/g))
						for (const lit of literals(holes[Number(m[1])].expression)) for (const q of problems(prop, lit)) report(line, `style="${prop}: ${shown}"`, q);
				}
			} else if (a.type === 'StyleDirective') {
				for (const p of parts) for (const t of written(p)) for (const q of problems(a.name, t)) report(line, `style:${a.name}="${t}"`, q);
			} else if (a.type === 'Attribute' && name === 'class') {
				for (const p of parts) {
					for (const t of written(p)) {
						for (const word of t.split(/\s+/).filter(Boolean)) {
							const q = [];
							if (hex.test(word)) q.push(`colour ${word.match(hex)[0]}`);
							for (const m of word.matchAll(new RegExp(`(\\d*\\.?\\d+)(${units})(?![a-z])`, 'gi'))) if (parseFloat(m[1]) !== 0) q.push(`length ${m[0]}`);
							for (const x of q) report(line, `class="${word}"`, x);
						}
					}
				}
			} else if (a.type === 'Attribute' && colourAttributes.has(name)) {
				// <rect fill="#ff0000">, <path stroke="red">: a colour that
				// CSS never sees, so no token can change it.
				for (const p of parts)
					for (const t of written(p)) {
						const v = t.trim();
						if (!v || keywordOnly.test(v)) continue;
						for (const q of problems('color', v)) report(line, `${name}="${v}"`, q);
					}
			} else if (a.type === 'Attribute' && styleAttributes.has(name)) {
				for (const p of parts)
					for (const t of p.type === 'ExpressionTag' ? literals(p.expression, true) : written(p))
						if (t.trim() && !keywordOnly.test(t.trim())) report(line, `${name}="${t.trim()}"`, `${name} ${t.trim()}`);
			} else if (a.type === 'Attribute' && (name === 'width' || name === 'height') && (element === 'img' || element === 'svg')) {
				// A size the code writes on an image or a drawing; one from
				// data (width={icon.width}) passes.
				for (const p of parts)
					for (const t of p.type === 'ExpressionTag' ? literals(p.expression, true) : written(p))
						if (t.trim()) report(line, `<${element} ${name}="${t.trim()}">`, `size ${t.trim()}`);
			}
		}
	});
}

function checkCss(file, text, report, name) {
	// Svelte's CSS parser reads plain CSS too, inside a <style> element.
	const wrapped = `<style>${text}</style>`;
	const ast = parse(wrapped, { modern: true, filename: file });
	for (const d of declarations(ast.css)) {
		const line = lineOf(text, d.start - '<style>'.length);
		noteUses(name, line, d.value, `${d.property}: ${d.value.trim()}`);
		if (d.property.startsWith('--')) {
			defined.add(d.property); // a token: its value is the one place it's written
			continue;
		}
		for (const p of problems(d.property, d.value)) report(line, `${d.property}: ${d.value.trim()}`, p);
	}
}

function files(dir) {
	return readdirSync(dir).flatMap((name) => {
		const p = join(dir, name);
		return statSync(p).isDirectory() ? files(p) : /\.(svelte|css)$/.test(name) ? [p] : [];
	});
}

const args = process.argv.slice(2);
const targets = args.length ? args.map((a) => join(process.cwd(), a)) : files(src);
const tokenFile = join(src, 'app.css');
const found = [];
// The token file first, so its tokens are known, even when not a target.
for (const file of [tokenFile, ...targets.filter((t) => t !== tokenFile)]) {
	const text = readFileSync(file, 'utf8');
	const name = relative(web, file).replaceAll('\\', '/');
	const target = targets.includes(file);
	const report = (line, where, what) => target && found.push(`${name}:${line}: hard-coded ${what} in ${where}`);
	if (file.endsWith('.svelte')) checkSvelte(file, text, report, name);
	else checkCss(file, text, report, name);
}
for (const u of uses) if (!defined.has(u.token)) found.push(`${u.name}:${u.line}: unknown token ${u.token} in ${u.where}`);
if (found.length) {
	console.error(found.join('\n'));
	console.error(`\n${found.length} problem(s): use a token from src/app.css instead (R82c, A73).`);
	process.exit(1);
}
console.log(`check-tokens: ${targets.length} files, no hard-coded colours, sizes, spacing, radii or type (R82c)`);
