// Labels for integrations (R47-R51c), shared by the owner's and admins' pages.
import type { Delivery, Integration, IntegrationKind } from './api.ts';

export const kindLabels: Record<IntegrationKind, string> = {
	webhook: 'Webhook',
	mqtt: 'MQTT',
	discord: 'Discord',
	telegram: 'Telegram',
	pushover: 'Pushover',
	ntfy: 'ntfy'
};

/** Presets send to third parties, so they default to Summary (R50a). */
export const presets: IntegrationKind[] = ['discord', 'telegram', 'pushover', 'ntfy'];

export function statusLabel(i: Pick<Integration, 'status' | 'paused_by'>): string {
	if (i.status === 'active') return 'Active';
	switch (i.paused_by) {
		case 'failing':
			return 'Paused: kept failing';
		case 'admin':
			return 'Paused by an admin';
		default:
			return 'Paused';
	}
}

export const deliveryLabels: Record<Delivery['status'], string> = {
	pending: 'Retrying',
	delivered: 'Delivered',
	failed: 'Failed',
	too_old: 'Skipped: too old',
	cancelled: 'Cancelled'
};

/** R47: shown on the integrations screen (and in docs/integrations.md). */
export const notice =
	'Integrations are a convenience, not a replacement for your pager: delivery is best-effort, and a page may arrive late or not at all.';
