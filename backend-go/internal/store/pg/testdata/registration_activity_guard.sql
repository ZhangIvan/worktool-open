-- Fixed-source helper for a fresh, pinned, empty-preload synthetic PG18.6 cluster.
-- The enclosing bootstrap_helper transaction binds POSTGRES_USER/OID 10 and the
-- exact target database before this file. No snapshot is an atomic admission fence.
CREATE SCHEMA wt_registration_admission AUTHORIZATION CURRENT_USER;
REVOKE ALL ON SCHEMA wt_registration_admission FROM PUBLIC;
GRANT USAGE ON SCHEMA wt_registration_admission TO wt_registration_lab_owner;

CREATE FUNCTION wt_registration_admission.isolated_owner_session_v1()
RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER VOLATILE PARALLEL UNSAFE
SET search_path = pg_catalog, pg_temp
AS $registration_activity_guard_v1$
DECLARE
    admitted boolean;
BEGIN
    PERFORM pg_catalog.pg_stat_clear_snapshot();
    WITH
-- BEGIN ACTIVITY_CLASSIFICATION_V1
activity AS MATERIALIZED (
    SELECT a.pid, a.backend_type, a.usesysid, a.usename, a.datid, a.datname,
           a.client_addr, a.client_hostname, a.client_port, a.leader_pid
    FROM pg_catalog.pg_stat_activity AS a
), classified AS MATERIALIZED (
    SELECT a.pid, a.backend_type,
        CASE
        WHEN a.backend_type IN ('checkpointer', 'background writer', 'walwriter',
                                'autovacuum launcher', 'io worker') THEN
            a.usesysid IS NULL AND a.usename IS NULL
            AND a.datid IS NULL AND a.datname IS NULL
            AND a.client_addr IS NULL AND a.client_hostname IS NULL
            AND a.client_port IS NULL AND a.leader_pid IS NULL
        WHEN a.backend_type = 'logical replication launcher' THEN
            a.usesysid = 10
            AND a.usename = (SELECT b.rolname FROM pg_catalog.pg_roles AS b WHERE b.oid = 10)
            AND a.datid IS NULL AND a.datname IS NULL
            AND a.client_addr IS NULL AND a.client_hostname IS NULL
            AND a.client_port IS NULL AND a.leader_pid IS NULL
        ELSE false
        END AS allowed
    FROM activity AS a
), background_admitted AS (
    SELECT
        NOT EXISTS (SELECT 1 FROM classified AS a
                    WHERE a.pid IS DISTINCT FROM pg_catalog.pg_backend_pid()
                    AND a.allowed IS NOT TRUE)
        AND (SELECT pg_catalog.count(*) FROM classified AS a WHERE a.backend_type = 'checkpointer') <= 1
        AND (SELECT pg_catalog.count(*) FROM classified AS a WHERE a.backend_type = 'background writer') <= 1
        AND (SELECT pg_catalog.count(*) FROM classified AS a WHERE a.backend_type = 'walwriter') <= 1
        AND (SELECT pg_catalog.count(*) FROM classified AS a WHERE a.backend_type = 'autovacuum launcher') <= 1
        AND (SELECT pg_catalog.count(*) FROM classified AS a WHERE a.backend_type = 'logical replication launcher') <= 1
        AND (SELECT pg_catalog.count(*) FROM classified AS a WHERE a.backend_type = 'io worker') <= 3 AS ok
)
-- END ACTIVITY_CLASSIFICATION_V1
    SELECT
        session_user = 'wt_registration_lab_owner'
        AND current_user = 'wt_registration_lab_activity_guard'
        AND pg_catalog.current_setting('server_version_num') = '180006'
        AND pg_catalog.current_database() ~ '^wt_test_[a-z0-9_]{8,48}$'
        AND (SELECT pg_catalog.count(*) = 1
             FROM pg_catalog.pg_roles AS b
             JOIN pg_catalog.pg_namespace AS n ON n.nspowner = b.oid
             WHERE b.oid = 10 AND b.rolname ~ '^wt_admin_[a-z0-9_]{16,40}$'
             AND b.rolsuper AND b.rolcanlogin
             AND n.nspname = 'wt_registration_admission')
        AND (SELECT pg_catalog.count(*) FROM activity AS a
             WHERE a.pid = pg_catalog.pg_backend_pid()) = 1
        AND (SELECT pg_catalog.count(*) = 1
             FROM activity AS a
             JOIN pg_catalog.pg_database AS d ON d.oid = a.datid AND d.datname = a.datname
             JOIN pg_catalog.pg_roles AS o ON o.oid = a.usesysid AND o.rolname = a.usename
             WHERE a.pid = pg_catalog.pg_backend_pid()
             AND a.backend_type = 'client backend' AND a.leader_pid IS NULL
             AND a.usename = 'wt_registration_lab_owner'
             AND d.datname = pg_catalog.current_database() AND d.datdba = o.oid)
        AND (SELECT b.ok FROM background_admitted AS b)
    INTO admitted;
    RETURN admitted IS TRUE;
END;
$registration_activity_guard_v1$;
ALTER FUNCTION wt_registration_admission.isolated_owner_session_v1() OWNER TO wt_registration_lab_activity_guard;
REVOKE ALL ON FUNCTION wt_registration_admission.isolated_owner_session_v1() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION wt_registration_admission.isolated_owner_session_v1() TO wt_registration_lab_owner;
