-- Bootstrap super admin. Seeds run on EVERY boot, so this statement is
-- insert-only: once a row holds the bootstrap email, dashboard edits (name,
-- password, decision, is_active) are never overwritten. If the SA row is
-- gone, the next boot fills the gap again from the fixed bootstrap hash
-- (bcrypt of "@Kidversafun"). The NOT EXISTS guard keeps the rule that an
-- email held by a non-SA row is never promoted or overwritten.
INSERT INTO users (school_id, name, email, password_hash, role, decision, is_active)
SELECT NULL, 'Super Admin', 'sa@example.com', '$2a$10$OhLt52Zg2DTMh8N7SRIX8eFeRrCbmhG7jTn8ZvgGnZmrNmHY/JtPq', 'SA', 'APPROVED', TRUE
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM users WHERE email = 'sa@example.com');
