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
// length does. A string in a style attribute's braces that holds
// declarations itself (style={"color: #f00"}) is read as one.
//
// And script code, a component's <script> and every .ts file under src:
// strings given to an element's style (el.style.color = …, .cssText = …,
// .style.setProperty(…)), and the strings and numbers given to a map
// style's colour, width, radius, size and font keys ('circle-radius',
// 'text-font', clusterRadius). A value from a function or a variable
// (token('--accent'), px('--map-stroke'), look.point) passes: it's read from
// a token, or checked where it's written.
// And src/app.css, where hard-coded values are allowed only as the value of
// a token (a custom property, --name: value); its other rules use tokens
// too. Selectors and @media, @container and @supports conditions aren't
// values: a breakpoint can't be a token in CSS. Unitless numbers (flex: 1,
// opacity), percentages, fr, times and angles are layout, not look.
// And every look in src/app.css sets the same tokens as the default one
// (R82d), so a second look leaves no default value behind.
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

/** The fixed values an expression can produce: its strings, the text of its
 * template literals, and (numbers too) its numbers, through arrays,
 * conditionals and joins. A function's result or a property read
 * (token('--accent'), look.clusters[1], m.colour) comes from elsewhere. */
function literals(expr, numbers = false) {
	const out = [];
	const visit = (n) => {
		if (!n) return;
		switch (n.type) {
			case 'Literal':
				if (typeof n.value === 'string' || (numbers && typeof n.value === 'number')) out.push(String(n.value));
				break;
			case 'TemplateLiteral':
				for (const q of n.quasis) if (q.value.cooked ?? q.value.raw) out.push(q.value.cooked ?? q.value.raw);
				n.expressions.forEach(visit);
				break;
			case 'ArrayExpression':
				n.elements.forEach(visit);
				break;
			case 'ConditionalExpression':
				visit(n.consequent);
				visit(n.alternate);
				break;
			case 'LogicalExpression':
			case 'BinaryExpression':
				visit(n.left);
				visit(n.right);
				break;
			case 'UnaryExpression':
				if (n.operator === '-' && n.argument.type === 'Literal' && numbers) out.push(`-${n.argument.value}`);
				break;
			case 'TSAsExpression':
			case 'TSSatisfiesExpression':
			case 'TSNonNullExpression':
			case 'ParenthesizedExpression':
				visit(n.expression);
				break;
		}
	};
	visit(expr);
	return out;
}

// Attributes that draw in a colour (SVG's presentation attributes, and the
// old HTML ones), and those that set a stroke, type or a corner's radius.
const colourAttributes = new Set(['fill', 'stroke', 'stop-color', 'flood-color', 'lighting-color', 'color', 'bgcolor', 'background', 'text']);
const styleAttributes = new Set(['stroke-width', 'stroke-dasharray', 'font-size', 'font-weight', 'font-family', 'letter-spacing', 'rx', 'ry']);
// Values an attribute may hold without naming a colour or a size.
const keywordOnly = /^(none|currentcolor|transparent|inherit|auto|context-fill|context-stroke)$/i;

/** Check text that holds style declarations ("color: #f00; padding: 4px"). */
function checkDeclarations(textValue, line, report, where) {
	if (!textValue.includes(':')) return;
	for (const [prop, val] of inline(textValue)) for (const q of problems(prop, val)) report(line, where(prop, val), q);
}

const kebab = (s) => s.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`);
const propName = (n) => (n.type === 'Identifier' ? n.name : n.type === 'Literal' && typeof n.value === 'string' ? n.value : null);
// A map style's keys that set a look ('circle-radius', 'text-font',
// 'circle-stroke-color', clusterRadius); British "colour" is data here.
const styleKey = /(color|width|radius|size|font)/i;

/**
 * Script code (a component's <script>, a .ts file) that styles: strings
 * given to an element's style (el.style.color = …, el.style.cssText = …),
 * to style.setProperty(…), and the strings and numbers given to a map
 * style's colour, width, radius, size and font keys. A value read from a
 * token (token('--accent'), px('--map-stroke')) passes.
 */
function checkScript(program, text, offset, report) {
	if (!program) return;
	const line = (n) => lineOf(text, n.start - offset);
	walk(program, (n) => {
		if (n.type === 'AssignmentExpression' && n.left.type === 'MemberExpression') {
			const target = n.left;
			const name = target.computed ? propName(target.property) : target.property.name;
			if (target.object.type === 'MemberExpression' && propName(target.object.property) === 'style' && name) {
				// el.style.color = '#f00'; el.style.cssText = 'color: #f00'
				for (const lit of literals(n.right)) {
					if (name === 'cssText') checkDeclarations(lit, line(n), report, (p, v) => `style.cssText = "${p}: ${v}"`);
					else for (const q of problems(kebab(name), lit)) report(line(n), `style.${name} = "${lit}"`, q);
				}
			} else if (name === 'style') {
				for (const lit of literals(n.right)) checkDeclarations(lit, line(n), report, (p, v) => `style = "${p}: ${v}"`);
			}
		} else if (n.type === 'CallExpression' && n.callee.type === 'MemberExpression' && propName(n.callee.property) === 'setProperty') {
			// el.style.setProperty('color', '#f00')
			const prop = n.arguments[0] && n.arguments[0].type === 'Literal' ? String(n.arguments[0].value) : 'color';
			for (const arg of n.arguments.slice(1)) for (const lit of literals(arg)) for (const q of problems(prop, lit)) report(line(n), `setProperty('${prop}', '${lit}')`, q);
		} else if (n.type === 'Property' && !n.computed && styleKey.test(propName(n.key) ?? '') && !/colour/i.test(propName(n.key))) {
			// 'circle-radius': 8, 'text-font': ['Noto Sans Medium'], 'circle-color': '#f00'
			const key = propName(n.key);
			for (const lit of literals(n.value, true)) {
				if (/^-?\d*\.?\d+$/.test(lit)) {
					if (parseFloat(lit) !== 0) report(line(n), `${key}: ${lit}`, `number ${lit}`);
				} else if (/font/i.test(key)) report(line(n), `${key}: '${lit}'`, `font ${lit}`);
				else for (const q of problems(key, lit)) report(line(n), `${key}: '${lit}'`, q);
			}
		}
	});
}

// Tokens defined in app.css, and those a component sets itself from data
// (style:--agency={…}): a var() naming anything else is a mistake.
const defined = new Set();
const uses = [];

function noteUses(name, line, value, where) {
	for (const m of value.matchAll(/var\(\s*(--[\w-]+)/g)) uses.push({ name, line, token: m[1], where });
}

function checkTs(file, text, report) {
	// Svelte's parser reads TypeScript too, inside a <script lang="ts">.
	const open = '<script lang="ts">';
	const ast = parse(`${open}${text}</script>`, { modern: true, filename: file });
	checkScript(ast.instance?.content, text, open.length, report);
}

function checkSvelte(file, text, report, name) {
	const ast = parse(text, { modern: true, filename: file });
	checkScript(ast.instance?.content, text, 0, report);
	checkScript(ast.module?.content, text, 0, report);
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
				// A string in the braces that holds declarations itself:
				// style={"color: #f00"}, style={`padding: 4px`}.
				for (const h of holes) for (const lit of literals(h.expression)) checkDeclarations(lit, line, report, (p, v) => `style={"${p}: ${v}"}`);
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

// R82d: a look is a whole set of tokens. The default look (Harbour) is the
// top-level :root rule that sets --bg; every other look, a rule
// :root[data-look='name'], sets exactly the same tokens, no fewer (or a
// default value would show through) and no more.
function checkLooks(file, text, name) {
	const wrapped = `<style>${text}</style>`;
	const ast = parse(wrapped, { modern: true, filename: file });
	const looks = [];
	for (const rule of ast.css.children) {
		if (rule.type !== 'Rule') continue;
		const selector = wrapped.slice(rule.prelude.start, rule.prelude.end).trim();
		const tokens = rule.block.children.filter((c) => c.type === 'Declaration' && c.property.startsWith('--')).map((c) => c.property);
		const look = selector === ':root' && tokens.includes('--bg') ? '' : /^:root\[data-look='([a-z]+)'\]$/.exec(selector)?.[1];
		if (look !== undefined) looks.push({ look, tokens: new Set(tokens), line: lineOf(text, rule.start - '<style>'.length) });
	}
	const problems = [];
	const base = looks.find((l) => l.look === '');
	if (!base) return [`${name}: no default look (a :root rule setting --bg)`];
	for (const l of looks.filter((l) => l.look !== '')) {
		for (const t of base.tokens) if (!l.tokens.has(t)) problems.push(`${name}:${l.line}: look ${l.look} doesn't set ${t}, which the default look sets`);
		for (const t of l.tokens) if (!base.tokens.has(t)) problems.push(`${name}:${l.line}: look ${l.look} sets ${t}, which the default look doesn't`);
	}
	return problems;
}

function files(dir) {
	return readdirSync(dir).flatMap((name) => {
		const p = join(dir, name);
		return statSync(p).isDirectory() ? files(p) : /\.(svelte|css|ts)$/.test(name) && !name.endsWith('.d.ts') ? [p] : [];
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
	else if (file.endsWith('.ts')) checkTs(file, text, report);
	else checkCss(file, text, report, name);
}
for (const u of uses) if (!defined.has(u.token)) found.push(`${u.name}:${u.line}: unknown token ${u.token} in ${u.where}`);
if (targets.includes(tokenFile)) found.push(...checkLooks(tokenFile, readFileSync(tokenFile, 'utf8'), relative(web, tokenFile).replaceAll('\\', '/')));
if (found.length) {
	console.error(found.join('\n'));
	console.error(`\n${found.length} problem(s): use a token from src/app.css instead, and give every look every token (R82c, R82d, A73).`);
	process.exit(1);
}
console.log(`check-tokens: ${targets.length} files, no hard-coded colours, sizes, spacing, radii or type (R82c)`);
