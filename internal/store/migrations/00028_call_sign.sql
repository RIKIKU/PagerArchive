-- +goose Up
-- R8 (ADR 0040; owner, 2026-11-17): the standard field `group` is now
-- `call_sign`, the paged unit's call sign, so "group" stays free for a group
-- of brigades. Each page keeps its value; only the names change. Integration
-- filters saved with a `group` filter keep it as `call_sign`.
ALTER TABLE messages RENAME COLUMN paging_group TO call_sign;
ALTER INDEX messages_group_idx RENAME TO messages_call_sign_idx;
UPDATE integrations
SET filter = (filter - 'group') || jsonb_build_object('call_sign', filter -> 'group')
WHERE filter ? 'group';

-- +goose Down
UPDATE integrations
SET filter = (filter - 'call_sign') || jsonb_build_object('group', filter -> 'call_sign')
WHERE filter ? 'call_sign';
ALTER INDEX messages_call_sign_idx RENAME TO messages_group_idx;
ALTER TABLE messages RENAME COLUMN call_sign TO paging_group;
