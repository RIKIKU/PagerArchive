-- +goose Up
-- R43a, R43d (ADR 0021). resent_at: when the invitation was last sent again
-- with a new link. link_lost_at: when the app found that its link no longer
-- works in Authentik, though nobody used or cancelled it (an invitation made
-- while they were single-use and opened by a mail provider's link scanner).
-- Such an invitation is shown as needing a resend, not as waiting.
-- cleared_at: when the app, after the invitation was used, cancelled or
-- expired, removed any unfinished Authentik user an enrolment with it left.
-- A resend clears it, since the invitation can be enrolled with again.
ALTER TABLE invitations ADD COLUMN resent_at timestamptz, ADD COLUMN link_lost_at timestamptz, ADD COLUMN cleared_at timestamptz;

-- +goose Down
ALTER TABLE invitations DROP COLUMN resent_at, DROP COLUMN link_lost_at, DROP COLUMN cleared_at;
