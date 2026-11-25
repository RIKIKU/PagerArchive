<script lang="ts">
	// R77b: "New release! Check out the release log", the first time a user
	// opens the app after a release is installed. The server says whether to
	// show it, and records it as told when it does, so a reload, another tab
	// or another device isn't told again; a build that isn't a release never
	// is. After 8 seconds, counted down by a line along its foot (longer while
	// a pointer or finger or focus is on it; at once on a tap, click or Esc) it
	// shrinks into what it's about: the version's tag at
	// the foot of the side menu, or More on a phone, whichever is on screen,
	// which then lights up (app.css's .release-landed). With reduced motion it
	// fades where it is instead. It's announced politely, and never takes
	// focus.
	import { onMount } from 'svelte';
	import { api } from './api.ts';
	import { menu } from './menu.svelte.ts';
	import { releaseHref } from './release.ts';

	const HOLD = 8000; // on screen (owner, 2026-11-19: "option A for sure": 8 seconds, was 3)
	const AFTER_HOLD = 1000; // the least it stays once the pointer or focus leaves it
	const FLY = 350; // the shrink into the tag ("quickly")
	const FADE = 150; // reduced motion: a quick fade
	const LIT = 1500; // the landing's highlight, as long as .release-landed's animation

	let version = $state('');
	let box: HTMLElement | undefined = $state();
	let line: HTMLElement | undefined = $state();
	let countdown: Animation | undefined;
	let left = 1; // the share of HOLD the line still shows
	let leaving = false;
	let timer: ReturnType<typeof setTimeout> | undefined;
	let remaining = HOLD;
	let since = 0;
	let pointer = false;
	let focused = false;

	onMount(() => {
		let gone = false;
		api
			.releaseSeen()
			.then((r) => {
				if (!gone && r.show && r.version) version = r.version;
			})
			.catch(() => {}); // no notice
		return () => {
			gone = true;
			clearTimeout(timer);
		};
	});

	// Its countdown starts when it's shown, and Esc sends it at once, as does a
	// tap or click on it. That listener is on the box itself, not left to
	// Svelte's delegation at the root: WebKit moves a tap to the nearest
	// thing that takes clicks (its touch adjustment), and with no listener of
	// its own the box isn't one, so a tap on it went to a message row behind.
	$effect(() => {
		const el = box;
		if (!el || !line) return;
		run();
		const key = (e: KeyboardEvent) => {
			if (e.key === 'Escape') leave();
		};
		el.addEventListener('click', leave);
		window.addEventListener('keydown', key);
		return () => {
			el.removeEventListener('click', leave);
			window.removeEventListener('keydown', key);
		};
	});

	// The line shrinks from what it shows to nothing over the time left, at a
	// steady pace, reduced motion or not: it tells the time left.
	function run() {
		since = Date.now();
		clearTimeout(timer);
		timer = setTimeout(leave, remaining);
		countdown?.cancel();
		countdown = line?.animate([{ transform: `scaleX(${left})` }, { transform: 'scaleX(0)' }], { duration: remaining, easing: 'linear', fill: 'forwards' });
	}
	// It waits while a pointer or finger, or keyboard focus, is on it, and so
	// does its line. A finger held on it waits, and lifting it is a tap, which
	// sends it (owner, 2026-11-19: accepted).
	function hold(on: { pointer?: boolean; focused?: boolean }) {
		if (leaving) return;
		const was = pointer || focused;
		pointer = on.pointer ?? pointer;
		focused = on.focused ?? focused;
		if (!was && (pointer || focused)) {
			clearTimeout(timer);
			const spent = Math.min(Date.now() - since, remaining);
			left *= 1 - spent / remaining;
			remaining -= spent;
			countdown?.pause();
		} else if (was && !pointer && !focused) {
			remaining = Math.max(remaining, AFTER_HOLD);
			run();
		}
	}

	/** What it lands on: the first that's on screen (the side menu's tag on a desktop, More on a phone). */
	function target() {
		return [...document.querySelectorAll<HTMLElement>('[data-release-target]')].find((el) => el.getClientRects().length > 0);
	}

	/** A menu scrolled so the tag is out of sight scrolls to show it, so it's seen to land. */
	function reveal(el: HTMLElement) {
		for (let p = el.parentElement; p; p = p.parentElement) {
			const o = getComputedStyle(p).overflowY;
			if ((o !== 'auto' && o !== 'scroll') || p.scrollHeight <= p.clientHeight) continue;
			const [a, b] = [el.getBoundingClientRect(), p.getBoundingClientRect()];
			if (a.bottom > b.bottom) p.scrollTop += a.bottom - b.bottom;
			else if (a.top < b.top) p.scrollTop -= b.top - a.top;
			return;
		}
	}

	async function leave() {
		const el = box;
		if (leaving || !el) return;
		leaving = true;
		clearTimeout(timer);
		countdown?.pause();
		const to = target();
		if (to) reveal(to);
		const hadFocus = el.contains(document.activeElement);
		const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
		let anim: Animation;
		if (reduced || !to) {
			el.dataset.leaving = 'fade';
			anim = el.animate([{ opacity: 1 }, { opacity: 0 }], { duration: FADE, easing: 'ease-out', fill: 'forwards' });
		} else {
			// Into the middle of the tag, about its size (kept on screen,
			// should the tag still be out of sight).
			const a = el.getBoundingClientRect();
			const b = to.getBoundingClientRect();
			const x = Math.min(Math.max(b.left + b.width / 2, 0), window.innerWidth);
			const y = Math.min(Math.max(b.top + b.height / 2, 0), window.innerHeight);
			const dx = x - (a.left + a.width / 2);
			const dy = y - (a.top + a.height / 2);
			const scale = Math.max(Math.min(b.width / a.width, b.height / a.height), 0.02);
			el.dataset.leaving = 'fly';
			anim = el.animate(
				[
					{ transform: 'none', opacity: 1 },
					{ opacity: 1, offset: 0.6 },
					{ transform: `translate(${dx}px, ${dy}px) scale(${scale})`, opacity: 0 }
				],
				{ duration: FLY, easing: 'cubic-bezier(0.5, 0, 0.75, 0)', fill: 'forwards' }
			);
		}
		try {
			await anim.finished;
		} catch {
			// cancelled: it goes all the same
		}
		version = '';
		if (!to) return;
		if (hadFocus) to.focus({ preventScroll: true });
		to.classList.remove('release-landed');
		void to.offsetWidth; // so the highlight starts again
		to.classList.add('release-landed');
		setTimeout(() => to.classList.remove('release-landed'), LIT);
	}
</script>

<div class="live" aria-live="polite" aria-atomic="true">
	{#if version}
		<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions (Esc does the same as a tap or click, from anywhere; the link is reached with Tab) -->
		<div
			class="notice"
			class:shrunk={menu.shrunk}
			data-testid="release-notice"
			bind:this={box}
			onpointerenter={() => hold({ pointer: true })}
			onpointerdown={() => hold({ pointer: true })}
			onpointerleave={() => hold({ pointer: false })}
			onpointercancel={() => hold({ pointer: false })}
			onfocusin={() => hold({ focused: true })}
			onfocusout={() => hold({ focused: false })}
		>
			New release! <a href={releaseHref(version)} target="_blank" rel="noopener noreferrer">Check out the release log</a>
			<span class="countdown" data-testid="release-countdown" aria-hidden="true" bind:this={line}></span>
		</div>
	{/if}
</div>

<style>
	/* Out of the frame's grid (R77d): it takes no cell of ArchiveUx's layout. */
	.live {
		position: fixed;
		z-index: var(--layer-overlay);
	}
	/* Beside the side menu, at the bottom: it covers no part of the menu. */
	.notice {
		position: fixed;
		left: calc(var(--menu-width) + var(--space-lg));
		bottom: var(--space-lg);
		z-index: var(--layer-overlay);
		max-width: min(var(--notice-width), var(--panel-max));
		padding: var(--space-sm) var(--space-md);
		border: var(--border-width-strong) solid var(--notice-edge);
		border-radius: var(--radius);
		background: var(--highlight);
		color: var(--fg);
		box-shadow: var(--notice-shadow);
		font-size: var(--text-sm);
		line-height: var(--leading-tight);
		cursor: default;
	}
	.notice.shrunk {
		left: calc(var(--menu-width-shrunk) + var(--space-lg));
	}
	.notice a {
		font-weight: var(--weight-strong);
	}
	/* The time left: a line along its foot, inside its corners, shrinking
	   towards the start. */
	.countdown {
		position: absolute;
		left: var(--space-sm);
		right: var(--space-sm);
		bottom: var(--space-3xs);
		height: var(--notice-countdown-height);
		border-radius: var(--radius-small);
		background: var(--notice-edge);
		transform-origin: left;
	}
	/* Phones (R81): above the bottom bar, across the width. */
	@media (max-width: 47.99rem) {
		.notice,
		.notice.shrunk {
			left: var(--page-pad-phone);
			right: var(--page-pad-phone);
			bottom: calc(var(--bottom-bar-height) + env(safe-area-inset-bottom) + var(--space-sm));
			max-width: none;
		}
	}
	@media print {
		.live {
			display: none;
		}
	}
</style>
