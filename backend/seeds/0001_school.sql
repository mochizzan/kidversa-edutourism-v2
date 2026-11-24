-- Bootstrap school: only on a fresh, empty schools table. Upgraded databases
-- already carry backfilled rows and must not receive an extra Kidversa school.
INSERT INTO schools (school_name, country, city, is_active)
SELECT 'Kidversa', 'Indonesia', NULL, TRUE
WHERE NOT EXISTS (SELECT 1 FROM schools);
