// Invitations as people see them (R43a, R43d): the inviter's list on the
// invite page, and a user's list on their page in Administration.
import type { Invitation } from './api.ts';

export const statusLabels: Record<Invitation['status'], string> = {
	pending: 'Waiting',
	expired: 'Expired',
	link_lost: 'Link no longer works',
	used: 'Joined',
	cancelled: 'Cancelled'
};

/** R43d: a waiting or expired invitation can be resent; a used or cancelled one can't. */
export const resendable = (inv: Invitation) => inv.status === 'pending' || inv.status === 'expired' || inv.status === 'link_lost';

/** What resending says when it's done. */
export const resentNotice = (inv: Invitation) => `Invitation sent to ${inv.email} again, with a new link for 7 days. The old link no longer works.`;
