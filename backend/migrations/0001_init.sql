-- 0001_init — full 05 §2 schema.
-- Rules (binding): ZERO FK constraints (all references are loose BIGINT
-- UNSIGNED, app-enforced); every domain row carries tenant_id with a
-- tenant-leading index (exceptions: tenants, migrations); utf8mb4_unicode_ci;
-- UTC datetimes; CHECKs mirror validate.go (defence in depth).
-- Surrogate-less tables (uniqueness-without-PK, 05 §2): session_members,
-- assessments, idempotency_keys. Composite PKs: mission_topics,
-- frame_programs, report_missions. The report_missions ≤4 mission rule is
-- app-enforced (E19 MISSION_LIMIT): a row-count CHECK does not exist in DDL.

CREATE TABLE IF NOT EXISTS tenants (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    name VARCHAR(160) NOT NULL,
    type ENUM('KOTA','NEGARA','LAINNYA') NOT NULL,
    is_active TINYINT(1) NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_tenants_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS users (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NULL,
    name VARCHAR(160) NOT NULL,
    email VARCHAR(320) NOT NULL,
    wa_number VARCHAR(20) NULL,
    password_hash VARCHAR(255) NOT NULL,
    role ENUM('SA','ADMIN','FASILITATOR') NOT NULL,
    decision ENUM('PENDING','APPROVED','REJECTED') NOT NULL DEFAULT 'PENDING',
    is_active TINYINT(1) NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_users_email (email),
    KEY idx_users_scope (tenant_id, role, decision, is_active)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS refresh_tokens (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    jti VARCHAR(64) NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    revoked_at TIMESTAMP NULL DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_refresh_jti (jti),
    KEY idx_refresh_user (user_id),
    KEY idx_refresh_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS reset_tokens (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NULL,
    user_id BIGINT UNSIGNED NOT NULL,
    code_hash CHAR(64) NOT NULL,
    code_plain VARCHAR(16) NULL,
    -- W1 addition: the 06 §1 E22 / 08 acceptance `mail_failed:true` marker the
    -- reset dashboard surfaces when SMTP is down (no other column can carry it).
    mail_failed TINYINT(1) NOT NULL DEFAULT FALSE,
    expires_at TIMESTAMP NOT NULL,
    used_at TIMESTAMP NULL DEFAULT NULL,
    revoked_at TIMESTAMP NULL DEFAULT NULL,
    revealed_at TIMESTAMP NULL DEFAULT NULL,
    revealed_by BIGINT UNSIGNED NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY idx_reset_tenant (tenant_id),
    KEY idx_reset_user_expires (user_id, expires_at),
    KEY idx_reset_expires (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS programs (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL,
    name VARCHAR(200) NOT NULL,
    description TEXT NULL,
    badge_name VARCHAR(200) NULL,
    badge_image_path VARCHAR(512) NULL,
    is_active TINYINT(1) NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    CONSTRAINT chk_programs_badge CHECK (
        (badge_name IS NULL AND badge_image_path IS NULL)
        OR (badge_name IS NOT NULL AND badge_image_path IS NOT NULL)),
    KEY idx_programs_tenant_active (tenant_id, is_active)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS topics (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL,
    program_id BIGINT UNSIGNED NOT NULL,
    program_name_snap VARCHAR(200) NOT NULL,
    name VARCHAR(200) NOT NULL,
    description TEXT NULL,
    sort_order INT NOT NULL DEFAULT 0,
    badge_name VARCHAR(200) NULL,
    badge_image_path VARCHAR(512) NULL,
    is_active TINYINT(1) NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    CONSTRAINT chk_topics_badge CHECK (
        (badge_name IS NULL AND badge_image_path IS NULL)
        OR (badge_name IS NOT NULL AND badge_image_path IS NOT NULL)),
    KEY idx_topics_program (program_id),
    KEY idx_topics_tenant_program_sort (tenant_id, program_id, sort_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS activities (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL,
    program_id BIGINT UNSIGNED NOT NULL,
    topic_id BIGINT UNSIGNED NOT NULL,
    program_name_snap VARCHAR(200) NOT NULL,
    topic_name_snap VARCHAR(200) NOT NULL,
    name VARCHAR(200) NOT NULL,
    indicator TEXT NULL,
    sort_order INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY idx_activities_program (program_id),
    KEY idx_activities_topic (topic_id),
    KEY idx_activities_tenant_topic_sort (tenant_id, topic_id, sort_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS missions (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL,
    program_id BIGINT UNSIGNED NOT NULL,
    program_name_snap VARCHAR(200) NOT NULL,
    name VARCHAR(200) NOT NULL,
    body TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY idx_missions_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS mission_topics (
    mission_id BIGINT UNSIGNED NOT NULL,
    topic_id BIGINT UNSIGNED NOT NULL,
    topic_name_snap VARCHAR(200) NOT NULL,
    tenant_id BIGINT UNSIGNED NOT NULL,
    PRIMARY KEY (mission_id, topic_id),
    KEY idx_mission_topics_topic (topic_id),
    KEY idx_mission_topics_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS frames (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL,
    image_path VARCHAR(512) NOT NULL,
    name VARCHAR(200) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY idx_frames_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS frame_programs (
    frame_id BIGINT UNSIGNED NOT NULL,
    program_id BIGINT UNSIGNED NOT NULL,
    program_name_snap VARCHAR(200) NOT NULL,
    tenant_id BIGINT UNSIGNED NOT NULL,
    PRIMARY KEY (frame_id, program_id),
    KEY idx_frame_programs_program (program_id),
    KEY idx_frame_programs_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS children (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL,
    name VARCHAR(200) NOT NULL,
    age TINYINT UNSIGNED NOT NULL,
    age_confirmed TINYINT(1) NOT NULL DEFAULT FALSE,
    school VARCHAR(200) NOT NULL,
    mother_name VARCHAR(160) NOT NULL,
    mother_wa VARCHAR(20) NOT NULL,
    father_name VARCHAR(160) NULL,
    father_wa VARCHAR(20) NULL,
    guardian_name VARCHAR(160) NULL,
    is_active TINYINT(1) NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    CONSTRAINT chk_children_age CHECK (age BETWEEN 1 AND 15),
    CONSTRAINT chk_children_age_confirm CHECK (age <= 10 OR age_confirmed = TRUE),
    KEY idx_children_tenant_active (tenant_id, is_active)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS sessions (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL,
    program_id BIGINT UNSIGNED NOT NULL,
    program_name_snap VARCHAR(200) NOT NULL,
    name VARCHAR(200) NOT NULL,
    description TEXT NULL,
    date DATE NOT NULL,
    start_time TIME NULL,
    end_time TIME NULL,
    location VARCHAR(200) NOT NULL,
    status ENUM('DRAFT','OPENED','FINISHED') NOT NULL DEFAULT 'DRAFT',
    created_by BIGINT UNSIGNED NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    CONSTRAINT chk_sessions_time CHECK (
        end_time IS NULL OR start_time IS NULL OR end_time > start_time),
    KEY idx_sessions_tenant_status_date (tenant_id, status, date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS session_topics (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL,
    session_id BIGINT UNSIGNED NOT NULL,
    topic_id BIGINT UNSIGNED NOT NULL,
    topic_name_snap VARCHAR(200) NOT NULL,
    sort_order INT NOT NULL DEFAULT 0,
    badge_name_snap VARCHAR(200) NULL,
    badge_image_snap VARCHAR(512) NULL,
    PRIMARY KEY (id),
    KEY idx_session_topics_session_sort (session_id, sort_order),
    KEY idx_session_topics_tenant_session (tenant_id, session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS session_activities (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL,
    session_id BIGINT UNSIGNED NOT NULL,
    session_topic_id BIGINT UNSIGNED NOT NULL,
    activity_id BIGINT UNSIGNED NOT NULL,
    activity_name_snap VARCHAR(200) NOT NULL,
    indicator_snap TEXT NULL,
    sort_order INT NOT NULL DEFAULT 0,
    PRIMARY KEY (id),
    KEY idx_session_activities_topic_sort (session_topic_id, sort_order),
    KEY idx_session_activities_session (session_id),
    KEY idx_session_activities_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS session_groups (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL,
    session_id BIGINT UNSIGNED NOT NULL,
    name VARCHAR(200) NOT NULL,
    facilitator_id BIGINT UNSIGNED NULL,
    facilitator_name_snap VARCHAR(160) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_session_groups_session (session_id),
    KEY idx_session_groups_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS session_members (
    session_id BIGINT UNSIGNED NOT NULL,
    group_id BIGINT UNSIGNED NOT NULL,
    child_id BIGINT UNSIGNED NOT NULL,
    child_name_snap VARCHAR(200) NOT NULL,
    tenant_id BIGINT UNSIGNED NOT NULL,
    UNIQUE KEY uq_session_members_child_session (child_id, session_id),
    KEY idx_session_members_session_group (session_id, group_id),
    KEY idx_session_members_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS assessments (
    session_id BIGINT UNSIGNED NOT NULL,
    session_activity_id BIGINT UNSIGNED NOT NULL,
    child_id BIGINT UNSIGNED NOT NULL,
    tenant_id BIGINT UNSIGNED NOT NULL,
    stars TINYINT UNSIGNED NOT NULL,
    note TEXT NULL,
    activity_name_snap VARCHAR(200) NOT NULL,
    child_name_snap VARCHAR(200) NOT NULL,
    assessor_id BIGINT UNSIGNED NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uq_assessments_activity_child (session_activity_id, child_id),
    KEY idx_assessments_session_child (session_id, child_id),
    KEY idx_assessments_tenant (tenant_id),
    CONSTRAINT chk_assessments_stars CHECK (stars BETWEEN 1 AND 4)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS photos (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL,
    session_id BIGINT UNSIGNED NOT NULL,
    session_topic_id BIGINT UNSIGNED NOT NULL,
    child_id BIGINT UNSIGNED NOT NULL,
    child_name_snap VARCHAR(200) NOT NULL,
    path VARCHAR(512) NOT NULL,
    frame_id BIGINT UNSIGNED NULL,
    is_candidate TINYINT(1) NOT NULL DEFAULT FALSE,
    file_size INT NOT NULL,
    mime VARCHAR(64) NOT NULL,
    created_by BIGINT UNSIGNED NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    file_ready TINYINT(1) NOT NULL DEFAULT TRUE,
    candidate_slot VARCHAR(128) GENERATED ALWAYS AS
        (IF(is_candidate = 1, CONCAT(session_id, ':', session_topic_id, ':', child_id), NULL)) VIRTUAL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_photos_candidate (candidate_slot),
    KEY idx_photos_session (session_id),
    KEY idx_photos_topic (session_topic_id),
    KEY idx_photos_child (child_id),
    KEY idx_photos_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS narratives (
    session_id BIGINT UNSIGNED NOT NULL,
    session_topic_id BIGINT UNSIGNED NOT NULL,
    child_id BIGINT UNSIGNED NOT NULL,
    tenant_id BIGINT UNSIGNED NOT NULL,
    text TEXT NOT NULL,
    model VARCHAR(64) NULL,
    generated_at TIMESTAMP NULL DEFAULT NULL,
    generated_by BIGINT UNSIGNED NULL,
    UNIQUE KEY uq_narratives_session_topic_child (session_id, session_topic_id, child_id),
    KEY idx_narratives_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS report_missions (
    session_id BIGINT UNSIGNED NOT NULL,
    session_topic_id BIGINT UNSIGNED NOT NULL,
    child_id BIGINT UNSIGNED NOT NULL,
    mission_id BIGINT UNSIGNED NOT NULL,
    mission_name_snap VARCHAR(200) NOT NULL,
    mission_body_snap TEXT NOT NULL,
    tenant_id BIGINT UNSIGNED NOT NULL,
    PRIMARY KEY (session_id, session_topic_id, child_id, mission_id),
    KEY idx_report_missions_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS consent_links (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL,
    session_id BIGINT UNSIGNED NOT NULL,
    child_id BIGINT UNSIGNED NOT NULL,
    token CHAR(64) NOT NULL,
    status ENUM('PENDING','SETUJU','DITOLAK') NOT NULL DEFAULT 'PENDING',
    respondent_name VARCHAR(160) NULL,
    responded_at TIMESTAMP NULL DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_consent_token (token),
    UNIQUE KEY uq_consent_session_child (session_id, child_id),
    KEY idx_consent_child (child_id),
    KEY idx_consent_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS public_links (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL,
    session_id BIGINT UNSIGNED NOT NULL,
    session_topic_id BIGINT UNSIGNED NOT NULL,
    child_id BIGINT UNSIGNED NOT NULL,
    kind ENUM('REPORT','GALLERY') NOT NULL,
    token CHAR(64) NOT NULL,
    revoked_at TIMESTAMP NULL DEFAULT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_public_token (token),
    KEY idx_public_links_session (session_id),
    KEY idx_public_links_child (child_id),
    KEY idx_public_links_tenant (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS wa_outbox (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    tenant_id BIGINT UNSIGNED NOT NULL,
    kind ENUM('REPORT','CONSENT') NOT NULL,
    target_wa VARCHAR(20) NOT NULL,
    payload_ref VARCHAR(512) NOT NULL,
    status ENUM('SENT','FAILED') NOT NULL,
    message_id VARCHAR(128) NULL,
    error TEXT NULL,
    idem_key VARCHAR(64) NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY idx_wa_outbox_status_created (status, created_at),
    KEY idx_wa_outbox_tenant_created (tenant_id, created_at),
    KEY idx_wa_outbox_idem (idem_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS idempotency_keys (
    route VARCHAR(200) NOT NULL,
    idem_key VARCHAR(64) NOT NULL,
    tenant_id BIGINT UNSIGNED NOT NULL,
    response MEDIUMTEXT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uq_idem_tenant_route_key (tenant_id, route, idem_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
