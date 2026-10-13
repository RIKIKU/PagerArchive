<script lang="ts">
	import { page } from '$app/state';
	import { api } from '#lib/api.ts';

	// Why a sign-in didn't work (codes from the server's /auth/ handlers).
	const reasons: Record<string, string> = {
		deactivated: 'Your account has been deactivated. Ask an admin if you think this is a mistake.',
		not_invited: "This sign-in isn't linked to a PagerArchive invitation. You need an invitation from an existing user.",
		invitation_cancelled: 'Your invitation was cancelled before you finished enrolling. Ask for a new one.',
		invitation_used: 'That invitation has already been used.',
		invitation_expired: 'Your invitation expired before you finished enrolling. Ask for a new one.',
		admin_exists: 'PagerArchive already has an admin. Ask an existing user for an invitation.',
		no_first_admin: "First-admin set-up isn't available.",
		directory_unavailable: "The sign-in service couldn't be reached. Try again shortly.",
		expired: 'The sign-in took too long or was interrupted. Try again.',
		refused: 'Sign-in was cancelled.',
		error: 'Something went wrong signing you in. Try again.'
	};
	const error = $derived(page.url.searchParams.get('error'));
	const signedOut = $derived(page.url.searchParams.has('signed_out'));
	let firstAdminOffered = $state(false);
	api
		.authStatus()
		.then((s) => (firstAdminOffered = s.first_admin_setup))
		.catch(() => {});
</script>

<svelte:head><title>Sign in · PagerArchive</title></svelte:head>

<h1>Sign in</h1>
{#if error}
	<p class="error" role="alert">{reasons[error] ?? reasons.error}</p>
{:else if signedOut}
	<p class="status" role="status">You've signed out.</p>
{/if}
<div class="box">
	<p>PagerArchive is invite-only. Sign in with the email address your invitation was sent to.</p>
	<a class="button" href="/auth/login">Sign in</a>
</div>
{#if firstAdminOffered}
	<section class="box" aria-labelledby="first-admin">
		<h2 id="first-admin">Set up the first admin</h2>
		<p>
			No admin exists yet. The address this deployment names as its first admin can enrol now, without an
			invitation. A code will be emailed to that address.
		</p>
		<button class="secondary" type="button" onclick={() => api.firstAdmin()}>Set up the first admin</button>
	</section>
{/if}

<style>
	.box {
		margin: 0.75rem;
		max-width: 36rem;
	}
	h2 {
		font-size: var(--text-lg);
		margin: 1.5rem 0 0.25rem;
	}
</style>
