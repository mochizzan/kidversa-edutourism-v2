-- 0004_school_rename — Tenant → School schema rename.
--
-- Historic 0001–0003 are immutable; fresh installs run 0001 → 0002 → 0003 →
-- 0004. DDL in MariaDB is not transactional, so every statement below is
-- written to be independently valid and the RENAME COLUMN / RENAME INDEX pair
-- for a table is kept in one ALTER. Deploy once, with a backup.
--
-- Zero FK constraints (app-enforced) and utf8mb4_unicode_ci are unchanged.
-- Column nullability and every index's column order are preserved verbatim by
-- RENAME COLUMN / RENAME INDEX (no drop/recreate).

-- 1. Entity table.
RENAME TABLE tenants TO schools;

-- 2. Entity shape. Dropping the unique index first removes the old name
--    constraint; school_name is intentionally NOT globally unique.
ALTER TABLE schools DROP INDEX uq_tenants_name;
ALTER TABLE schools RENAME COLUMN name TO school_name;
ALTER TABLE schools MODIFY COLUMN school_name VARCHAR(200) NOT NULL;
ALTER TABLE schools DROP COLUMN type;
ALTER TABLE schools ADD COLUMN country VARCHAR(100) NOT NULL DEFAULT 'Indonesia' AFTER school_name;
ALTER TABLE schools ADD COLUMN city VARCHAR(100) NULL AFTER country;
ALTER TABLE schools ADD KEY idx_schools_school_name (school_name);

-- 3. Backfill existing rows. Country default and city NULL are already right.
UPDATE schools SET school_name = 'Kidversa';

-- 4. tenant_id → school_id on all 24 referencing tables. Nullability is kept:
--    users, refresh_tokens and reset_tokens stay NULL; all others NOT NULL.
ALTER TABLE users
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_users_scope TO idx_users_school_scope;

ALTER TABLE refresh_tokens
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_refresh_tenant TO idx_refresh_school;

ALTER TABLE reset_tokens
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_reset_tenant TO idx_reset_school;

ALTER TABLE programs
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_programs_tenant_active TO idx_programs_school_active;

ALTER TABLE topics
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_topics_tenant_program_sort TO idx_topics_school_program_sort;

ALTER TABLE activities
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_activities_tenant_topic_sort TO idx_activities_school_topic_sort;

ALTER TABLE missions
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_missions_tenant TO idx_missions_school;

ALTER TABLE mission_topics
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_mission_topics_tenant TO idx_mission_topics_school;

ALTER TABLE frames
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_frames_tenant TO idx_frames_school;

ALTER TABLE frame_programs
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_frame_programs_tenant TO idx_frame_programs_school;

ALTER TABLE children
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_children_tenant_active TO idx_children_school_active;

ALTER TABLE sessions
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_sessions_tenant_status_date TO idx_sessions_school_status_date;

ALTER TABLE session_topics
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_session_topics_tenant_session TO idx_session_topics_school_session;

ALTER TABLE session_activities
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_session_activities_tenant TO idx_session_activities_school;

ALTER TABLE session_groups
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_session_groups_tenant TO idx_session_groups_school;

ALTER TABLE session_members
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_session_members_tenant TO idx_session_members_school;

ALTER TABLE assessments
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_assessments_tenant TO idx_assessments_school;

ALTER TABLE photos
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_photos_tenant TO idx_photos_school;

ALTER TABLE narratives
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_narratives_tenant TO idx_narratives_school;

ALTER TABLE report_missions
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_report_missions_tenant TO idx_report_missions_school;

ALTER TABLE consent_links
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_consent_tenant TO idx_consent_school;

ALTER TABLE public_links
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_public_links_tenant TO idx_public_links_school;

ALTER TABLE wa_outbox
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX idx_wa_outbox_tenant_created TO idx_wa_outbox_school_created;

ALTER TABLE idempotency_keys
    RENAME COLUMN tenant_id TO school_id,
    RENAME INDEX uq_idem_tenant_route_key TO uq_idem_school_route_key;
