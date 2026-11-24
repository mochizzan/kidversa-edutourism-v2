-- 0002_users_photo_path — 09-PROFILE-PHOTOS: user-keyed avatar.
-- Nullable (no photo yet); VARCHAR(512) matches the existing path column
-- widths (photos.image_path, frames.image_path). Covers SA's tenant-null
-- case: the path is keyed by the account, not the tenant.
ALTER TABLE users
    ADD COLUMN photo_path VARCHAR(512) NULL;
