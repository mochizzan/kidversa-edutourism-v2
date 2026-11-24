-- 0005_mission_body_removal.sql — Mission content (`body`) removed.
--
-- A mission is now just its name: the bank form no longer captures a
-- description and the report renders only `mission_name_snap`. Historic
-- picks lose their content snapshot with the column (deploy once, with a
-- backup). 0001–0004 stay immutable; fresh installs run 0001 → … → 0005.
--
-- DDL in MariaDB is not transactional, but each statement below is
-- independently valid.

ALTER TABLE missions DROP COLUMN body;

ALTER TABLE report_missions DROP COLUMN mission_body_snap;
