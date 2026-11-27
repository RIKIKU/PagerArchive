// R82g: every form's own check (ArchiveUx's design-system.md, section 5;
// owner, 2026-11-20: "The word required, every form."). A form has
// novalidate, so the browser shows none of its own bubbles; on save it runs
// check(): each control the browser finds invalid (required, type, min, max,
// maxlength…), and each of the form's own rules (a group, a condition), gets
// a message under it, aria-invalid and aria-describedby to that message.
// At the top, ArchiveUx's ErrorSummary (FormSummary.svelte; its forms rule
// 4, 0.6.0) names each problem's field and links to it (listed), and focus
// moves to the summary. A control's problem goes when it's changed
// (cleared()), with its link, or at the next save.
import { tick } from 'svelte';

type Control = HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;

/** The id of the message under the control or group with this id. */
export const errorId = (id: string) => `${id}-error`;

/** What to do about a control the browser finds invalid, in words. */
export function messageFor(el: Control): string {
	const v = el.validity;
	if (el.dataset.problem && !v.valid && !v.customError) return el.dataset.problem;
	if (v.valueMissing) {
		if (el instanceof HTMLSelectElement || (el instanceof HTMLInputElement && el.type === 'radio')) return 'Choose one.';
		if (el instanceof HTMLInputElement && el.type === 'checkbox') return 'Tick this.';
		if (el instanceof HTMLInputElement && el.type === 'file') return 'Choose a file.';
		return 'Fill this in.';
	}
	if (v.typeMismatch) {
		if (el.type === 'email') return 'Enter an email address, like name@example.org.';
		if (el.type === 'url') return 'Enter a whole address, starting https:// or http://.';
	}
	if (v.badInput || v.stepMismatch) return 'Enter a whole number.';
	if (el instanceof HTMLInputElement) {
		if (v.rangeUnderflow || v.rangeOverflow) {
			return el.min && el.max ? `Enter a number from ${el.min} to ${el.max}.` : v.rangeUnderflow ? `Enter ${el.min} or more.` : `Enter ${el.max} or less.`;
		}
	}
	if (!(el instanceof HTMLSelectElement)) {
		if (v.tooLong) return `Use at most ${el.maxLength} characters.`;
		if (v.tooShort) return `Use at least ${el.minLength} characters.`;
	}
	if (v.patternMismatch) return el.title || 'Check how this is written.';
	return el.validationMessage || 'Check this.';
}

const isControl = (el: Element): el is Control =>
	el instanceof HTMLInputElement || el instanceof HTMLSelectElement || el instanceof HTMLTextAreaElement;

const firstControl = (el: Element): Control | null =>
	isControl(el) ? el : el.querySelector<Control>('input:not([disabled]), select:not([disabled]), textarea:not([disabled])');

/**
 * A field's name as its label or legend says it, without the Required tag,
 * its "when" or a hint in brackets: "Name", "Which pages".
 */
export function fieldLabel(el: Element): string {
	const named = isControl(el) ? el.labels?.[0] : el instanceof HTMLFieldSetElement ? el.querySelector(':scope > legend') : null;
	if (!named) return el.getAttribute('aria-label') ?? '';
	const copy = named.cloneNode(true) as Element;
	for (const extra of Array.from(copy.querySelectorAll('.required, .required-when, .hint, input, select, textarea'))) extra.remove();
	return (copy.textContent ?? '').replace(/\s+/g, ' ').trim();
}

/** A problem as the error summary lists it: the control its link moves to, its field's name, what to do. */
export type Listed = { id: string; label: string; message: string };

/** One form's problems, by the id of the control or group each is under. */
export class FormCheck {
	problems: Record<string, string> = $state({});
	/** The problems found at the last save, in the form's order: each one's key, its link's control and its field's name. */
	#found: { key: string; id: string; label: string }[] = $state([]);
	/** Moves focus to the form's summary (FormSummary sets it). */
	#focusSummary: (() => void) | undefined;

	/** The error summary's problems, in the form's order (those not cleared since the save). */
	get listed(): Listed[] {
		return this.#found.filter((f) => this.problems[f.key]).map((f) => ({ id: f.id, label: f.label, message: this.problems[f.key] }));
	}

	/** FormSummary's way to take the focus after a save with problems. */
	summaryFocus(focus: (() => void) | undefined) {
		this.#focusSummary = focus;
	}

	/** aria-invalid for the control with this id. */
	invalid = (id: string): 'true' | undefined => (this.problems[id] ? 'true' : undefined);

	/** aria-describedby for the control or group with this id: its hints, then its message. */
	describedBy = (id: string, ...hints: (string | false | null | undefined)[]): string | undefined => {
		const ids = [...hints.filter((h): h is string => !!h), ...(this.problems[id] ? [errorId(id)] : [])];
		return ids.length ? ids.join(' ') : undefined;
	};

	/**
	 * Checks the form: the browser's own constraints on each control with an
	 * id, then rules, the form's own (by id, a message, or nothing when the
	 * rule holds). Shows each problem, lists it in the summary, moves focus
	 * to the summary (to the first problem in a form without one), and says
	 * if the form can be sent.
	 */
	check(form: HTMLFormElement, rules: Record<string, string | false | null | undefined> = {}): boolean {
		const found: Record<string, string> = {};
		for (const el of Array.from(form.elements)) {
			if (!isControl(el) || !el.id || !el.willValidate) continue;
			if (!el.checkValidity()) found[el.id] = messageFor(el);
		}
		for (const [id, message] of Object.entries(rules)) if (message && !found[id]) found[id] = message;
		const shown = Object.keys(found)
			.map((key) => ({ key, el: document.getElementById(key) }))
			.filter((p): p is { key: string; el: HTMLElement } => !!p.el)
			.sort((a, b) => (a.el.compareDocumentPosition(b.el) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1));
		// A group's link goes to its first control (the one its problem is about).
		this.#found = shown.map(({ key, el }) => ({ key, id: firstControl(el)?.id || key, label: fieldLabel(el) }));
		this.problems = found;
		const first = shown[0]?.el;
		if (!first) return true;
		// After the messages, aria-describedby and the summary are drawn, so moving there reads them.
		tick().then(() => {
			if (this.#focusSummary) return this.#focusSummary();
			(firstControl(first) ?? first).focus();
		});
		return false;
	}

	/**
	 * On input or change in the form: the changed control's problem goes, and,
	 * for a ticked box or a chosen radio, its group's (other groups' problems
	 * wait for the next save: typing in a search isn't choosing).
	 */
	cleared = (ev: Event) => {
		const el = ev.target;
		if (!(el instanceof Element)) return;
		const box = el instanceof HTMLInputElement && (el.type === 'checkbox' || el.type === 'radio');
		const ids = Object.keys(this.problems).filter((id) => id === el.id || (box && document.getElementById(id)?.contains(el)));
		if (!ids.length) return;
		const next = { ...this.problems };
		for (const id of ids) delete next[id];
		this.problems = next;
	};

	/** Clears every problem (a form closed or reset). */
	reset() {
		this.problems = {};
		this.#found = [];
	}
}
