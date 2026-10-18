-- One-time setup to run the example in database.tenancy.mode: rls.
-- The compose user `app` is a superuser, and superusers ignore every RLS
-- policy, so nForge refuses to start in rls mode with it. This creates a
-- regular role for the application:
--
--   docker exec -i postgres psql -U app -d app < _examples/database/rls-setup.sql
DO $$
BEGIN
	IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'app_tenant') THEN
		CREATE ROLE app_tenant LOGIN PASSWORD 'app_tenant' NOSUPERUSER NOBYPASSRLS;
	END IF;
END $$;

GRANT USAGE, CREATE ON SCHEMA public TO app_tenant;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app_tenant;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_tenant;

-- notes gets its policy at startup (ensureNotesSchema), which only the
-- table owner may do. If a column-mode run already created it as `app`,
-- hand it over (its id sequence follows the table).
ALTER TABLE IF EXISTS notes OWNER TO app_tenant;
