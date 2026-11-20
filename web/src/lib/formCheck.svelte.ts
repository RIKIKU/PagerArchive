// R82g: every form's own check (ArchiveUx's design-system.md, section 5;
// owner, 2026-11-20: "The word required, every form."). A form has
// novalidate, so the browser shows none of its own bubbles; on save it runs
// check(): each control the browser finds invalid (required, type, min, max,
// maxlength…), and each of the form's own rules (a group, a condition), gets
// a message under it, aria-invalid and aria-describedby to that message;
// focus moves to the first; and the top line only counts them. A control's
// problem goes when it's changed (cleared()), or at the next save.
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

/** One form's problems, by the id of the control or group each is under. */
export class FormCheck {
	problems: Record<string, string> = $state({});

	/** "2 fields need attention", or nothing. */
	get summary(): string {
		const n = Object.keys(this.problems).length;
		return n ? `${n} ${n === 1 ? 'field needs' : 'fields need'} attention` : '';
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
	 * rule holds). Shows each problem, moves focus to the first, and says if
	 * the form can be sent.
	 */
	check(form: HTMLFormElement, rules: Record<string, string | false | null | undefined> = {}): boolean {
		const found: Record<string, string> = {};
		for (const el of Array.from(form.elements)) {
			if (!isControl(el) || !el.id || !el.willValidate) continue;
			if (!el.checkValidity()) found[el.id] = messageFor(el);
		}
		for (const [id, message] of Object.entries(rules)) if (message && !found[id]) found[id] = message;
		this.problems = found;
		const first = Object.keys(found)
			.map((id) => document.getElementById(id))
			.filter((el): el is HTMLElement => !!el)
			.sort((a, b) => (a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1))[0];
		if (!first) return true;
		// After the messages and aria-describedby are drawn, so moving there reads them.
		tick().then(() => {
			const target = isControl(first) ? first : first.querySelector<HTMLElement>('input:not([disabled]), select:not([disabled]), textarea:not([disabled])');
			(target ?? first).focus();
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
	}
}
