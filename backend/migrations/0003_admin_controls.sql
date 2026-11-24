-- 0003: admin impersonation + administrative password-reset ledgers.
--
-- Impersonation ledger: the refresh_tokens row itself records the actor.
--   impersonated_by   = users.id of the admin who started the switch
--   impersonation_root = STABLE key for one impersonation session; copied
--                        unchanged onto every rotated row, so a refresh does
--                        not fragment or lose the record. It equals the actor's
--                        revoked jti when the caller had a refresh cookie,
--                        otherwise a random 16-byte hex generated at switch
--                        time.
--
-- Password-change ledger: reset_tokens rows distinguish self-service codes
-- (source='SELF') from admin resets (source='ADMIN', created_by = actor id).
--
-- No FK constraints (project rule): all references are loose BIGINT UNSIGNED,
-- app-enforced.
ALTER TABLE refresh_tokens
    ADD COLUMN impersonated_by    BIGINT UNSIGNED NULL AFTER user_id,
    ADD COLUMN impersonation_root VARCHAR(64)     NULL AFTER impersonated_by,
    ADD KEY idx_refresh_impersonated (impersonated_by, created_at),
    ADD KEY idx_refresh_imp_root      (impersonation_root);

ALTER TABLE reset_tokens
    ADD COLUMN source     ENUM('SELF','ADMIN') NOT NULL DEFAULT 'SELF' AFTER user_id,
    ADD COLUMN created_by BIGINT UNSIGNED      NULL AFTER source,
    ADD KEY idx_reset_source (source, created_at);
