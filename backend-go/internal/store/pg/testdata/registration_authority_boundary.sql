-- LOCAL SQL IMPLEMENTATION CANDIDATE, NEVER EXECUTED BY THIS TASK.
-- Testdata only, not a migration, startup hook or deployment permission.
-- Future installation changes ACLs in ONE NEW disposable wt_test_* database.
-- Its separate trusted bootstrap must pre-provision the five exact lab roles,
-- a fresh database owned by wt_registration_lab_owner, baseline 0001..0010,
-- and fresh_registration_registry_candidate.sql. No ambient DSN or adoption.
-- No roles/credentials are created here. Never load this in an existing service.
-- The importer validates all typed values against frozen Go canonical bytes.
-- SQL copies those trusted values; hashes/strings do not authenticate approval,
-- provider mapping, historical freshness, a writer fence or a durable barrier.
-- This SQL does not implement a production authority or unknown-outcome retry.
-- Execute this entire script once, without an outer transaction, only after
-- separate run approval. Fail/rollback on the first error. Installation is atomic.

BEGIN;
SET LOCAL search_path TO pg_catalog, wt_synthetic_registry, pg_temp;
SET LOCAL lock_timeout = '1s';
SET LOCAL statement_timeout = '5s';

DO $installation_guard$
DECLARE
  owner_oid oid;
  ns_oid oid;
  role_record record;
  role_count integer;
  dependency text;
BEGIN
  IF session_user IS DISTINCT FROM 'wt_registration_lab_owner'
     OR current_user IS DISTINCT FROM session_user
     OR pg_catalog.current_database() COLLATE pg_catalog."C" !~ '^wt_test_[a-z0-9_]{1,55}$'
     OR pg_catalog.current_setting('server_version_num') IS DISTINCT FROM '180006' THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_INSTALLER_REQUIRED';
  END IF;
  SELECT r.oid INTO STRICT owner_oid FROM pg_catalog.pg_roles AS r WHERE r.rolname = session_user;
  SELECT n.oid INTO ns_oid FROM pg_catalog.pg_namespace AS n
    WHERE n.nspname = 'wt_synthetic_registry' AND n.nspowner = owner_oid;
  IF ns_oid IS NULL OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_database AS d
      WHERE d.datname = pg_catalog.current_database() AND d.datdba = owner_oid)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_namespace AS n,
       LATERAL pg_catalog.aclexplode(COALESCE(n.nspacl, pg_catalog.acldefault('n', n.nspowner))) AS a
       WHERE n.oid = ns_oid AND a.privilege_type = 'CREATE' AND a.grantee <> owner_oid) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_OWNERSHIP_REQUIRED';
  END IF;
  SELECT pg_catalog.count(*) INTO role_count FROM pg_catalog.pg_roles AS r WHERE r.rolname IN
    ('wt_registration_lab_owner', 'wt_registration_lab_issuer', 'wt_registration_lab_consumer',
     'wt_registration_lab_routine', 'wt_registration_lab_unrelated');
  IF role_count <> 5 THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_ROLES_REQUIRED';
  END IF;
  FOR role_record IN SELECT * FROM pg_catalog.pg_roles AS r WHERE r.rolname IN
    ('wt_registration_lab_owner', 'wt_registration_lab_issuer', 'wt_registration_lab_consumer',
     'wt_registration_lab_routine', 'wt_registration_lab_unrelated') LOOP
    IF role_record.rolsuper OR role_record.rolcreaterole OR role_record.rolcreatedb
       OR role_record.rolreplication OR role_record.rolbypassrls OR role_record.rolinherit
       OR role_record.rolcanlogin IS DISTINCT FROM (role_record.rolname <> 'wt_registration_lab_routine') THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_ROLE_ATTRIBUTES';
    END IF;
  END LOOP;
  -- Only the trusted installer may SET ROLE to the nonlogin routine owner,
  -- solely to support transfer of routine ownership. No ADMIN/INHERIT route.
  IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members AS m
       JOIN pg_catalog.pg_roles AS r ON r.oid = m.roleid
       WHERE m.member = owner_oid AND r.rolname = 'wt_registration_lab_routine'
         AND m.set_option AND NOT m.admin_option AND NOT m.inherit_option)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members AS m
       JOIN pg_catalog.pg_roles AS granted ON granted.oid = m.roleid
       JOIN pg_catalog.pg_roles AS member_role ON member_role.oid = m.member
       WHERE (granted.rolname IN ('wt_registration_lab_owner', 'wt_registration_lab_issuer',
              'wt_registration_lab_consumer', 'wt_registration_lab_routine', 'wt_registration_lab_unrelated')
          OR member_role.rolname IN ('wt_registration_lab_owner', 'wt_registration_lab_issuer',
              'wt_registration_lab_consumer', 'wt_registration_lab_routine', 'wt_registration_lab_unrelated'))
         AND NOT (m.member = owner_oid AND granted.rolname = 'wt_registration_lab_routine'
                  AND m.set_option AND NOT m.admin_option AND NOT m.inherit_option)) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_MEMBERSHIP_REQUIRED';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_class AS c WHERE c.relnamespace = ns_oid
        AND (c.relowner <> owner_oid OR c.relkind NOT IN ('r','i','S') OR c.relpersistence <> 'p'
             OR c.relrowsecurity OR c.relforcerowsecurity))
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_proc AS p WHERE p.pronamespace = ns_oid
        AND (p.proowner <> owner_oid OR p.prosecdef))
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_rewrite AS r JOIN pg_catalog.pg_class AS c ON c.oid = r.ev_class
        WHERE c.relnamespace = ns_oid)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_constraint AS c WHERE c.connamespace = ns_oid AND NOT c.convalidated)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_trigger AS t JOIN pg_catalog.pg_class AS c ON c.oid = t.tgrelid
        WHERE c.relnamespace = ns_oid AND t.tgenabled <> 'O') THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_DEPENDENCY_POSTURE';
  END IF;
  FOREACH dependency IN ARRAY ARRAY[
    'scope_limits', 'scope_slots', 'developer_apps', 'callback_destinations',
    'callback_destination_versions', 'callback_registration_policies', 'callback_registration_keys',
    'callback_registration_bindings', 'callback_registration_receipts', 'callback_subscription_versions',
    'admission_reservations', 'callback_outbox', 'commands', 'callback_replay_receipts'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_class AS c
       WHERE c.relnamespace = ns_oid AND c.relname = dependency AND c.relkind = 'r') THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_BASELINE_REQUIRED';
    END IF;
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_class AS c WHERE c.relnamespace = ns_oid AND c.relname IN
       ('registration_prepared_grants','registration_authority_control','registration_grant_consumptions'))
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_proc AS p WHERE p.pronamespace = ns_oid AND p.proname IN
       ('consume_prepared_fresh_registration_v1','reject_registration_authority_mutation',
        'guard_registration_control_update')) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_OBJECT_EXISTS';
  END IF;
  -- This boundary is installed before any destination/history, never over an
  -- existing service or a restored schema. This is an empty lab check, not proof
  -- that production history never existed. The external run manifest proves newness.
  IF EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_destinations)
     OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_destination_versions)
     OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_registration_policies)
     OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_registration_keys)
     OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_registration_bindings)
     OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_registration_receipts)
     OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_subscription_versions)
     OR EXISTS (SELECT 1 FROM wt_synthetic_registry.admission_reservations)
     OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_outbox)
     OR EXISTS (SELECT 1 FROM wt_synthetic_registry.commands)
     OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_replay_receipts) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_NOT_FRESH';
  END IF;
END
$installation_guard$;

CREATE TABLE wt_synthetic_registry.registration_prepared_grants (
  grant_id text NOT NULL CHECK (grant_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  authority_id text NOT NULL CHECK (authority_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  operation_id text NOT NULL CHECK (operation_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  tenant text NOT NULL CHECK (tenant COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  app text NOT NULL CHECK (app COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  destination_id text NOT NULL CHECK (destination_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  format text NOT NULL CHECK (format = 'worktool-synthetic-prepared-registration-v1'),
  evidence_kind text NOT NULL CHECK (evidence_kind = 'SYNTHETIC_ONLY'),
  action text NOT NULL CHECK (action = 'REGISTER_FRESH_PENDING'),
  expected_revision bigint NOT NULL CHECK (expected_revision = 0),
  new_revision bigint NOT NULL CHECK (new_revision = 1),
  database_name text NOT NULL CHECK (database_name COLLATE pg_catalog."C" ~ '^wt_test_[a-z0-9_]{1,55}$'),
  database_oid oid NOT NULL CHECK (database_oid <> 0),
  schema_name text NOT NULL CHECK (schema_name = 'wt_synthetic_registry'),
  schema_oid oid NOT NULL CHECK (schema_oid <> 0),
  environment_id text NOT NULL CHECK (environment_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  deployment_generation text NOT NULL CHECK (deployment_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  recovery_generation text NOT NULL CHECK (recovery_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  routine_definition_sha256 text NOT NULL CHECK (routine_definition_sha256 COLLATE pg_catalog."C" ~ '^[0-9a-f]{64}$'),
  source_manifest_sha256 text NOT NULL CHECK (source_manifest_sha256 COLLATE pg_catalog."C" ~ '^[0-9a-f]{64}$'),
  issuer_role_name text NOT NULL CHECK (issuer_role_name = 'wt_registration_lab_issuer'),
  issuer_role_oid oid NOT NULL CHECK (issuer_role_oid <> 0),
  consumer_role_name text NOT NULL CHECK (consumer_role_name = 'wt_registration_lab_consumer'),
  consumer_role_oid oid NOT NULL CHECK (consumer_role_oid <> 0),
  authority_policy_id text NOT NULL CHECK (authority_policy_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  actor text NOT NULL CHECK (actor COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  actor_generation text NOT NULL CHECK (actor_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  approval_ref text NOT NULL CHECK (approval_ref COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  approval_digest text NOT NULL CHECK (approval_digest COLLATE pg_catalog."C" ~ '^[0-9a-f]{64}$'),
  approval_generation text NOT NULL CHECK (approval_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  fence_ref text NOT NULL CHECK (fence_ref COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  fence_generation text NOT NULL CHECK (fence_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  history_ref text NOT NULL CHECK (history_ref COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  history_digest text NOT NULL CHECK (history_digest COLLATE pg_catalog."C" ~ '^[0-9a-f]{64}$'),
  history_generation text NOT NULL CHECK (history_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  provider_attestation_ref text NOT NULL CHECK (provider_attestation_ref COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  provider_mapping_generation text NOT NULL CHECK (provider_mapping_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  barrier_generation text NOT NULL CHECK (barrier_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  boot_generation text NOT NULL CHECK (boot_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  issued_at timestamptz NOT NULL CHECK (pg_catalog.isfinite(issued_at)),
  not_before timestamptz NOT NULL CHECK (pg_catalog.isfinite(not_before)),
  expires_at timestamptz NOT NULL CHECK (pg_catalog.isfinite(expires_at)),
  plan_identity text NOT NULL CHECK (plan_identity = '' OR plan_identity COLLATE pg_catalog."C" ~ '^cbplan-v1:sha256:[0-9a-f]{64}$'),
  policy_version bigint NOT NULL CHECK (policy_version > 0),
  key_version bigint NOT NULL CHECK (key_version > 0),
  canonical_url text NOT NULL CHECK (pg_catalog.octet_length(canonical_url) BETWEEN 1 AND 2048),
  cidrs text[] NOT NULL CHECK (pg_catalog.array_ndims(cidrs) = 1
    AND pg_catalog.array_lower(cidrs, 1) = 1 AND pg_catalog.cardinality(cidrs) BETWEEN 1 AND 32
    AND pg_catalog.array_position(cidrs, NULL::text) IS NULL),
  signature_profile text NOT NULL CHECK (signature_profile = 'worktool-callback-hmac-sha256-v1'),
  key_ref text NOT NULL CHECK (key_ref COLLATE pg_catalog."C" ~ '^[!-~]{1,256}$'),
  provider_authority_id text NOT NULL CHECK (provider_authority_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  revision_kind text NOT NULL CHECK (revision_kind = 'exact'),
  revision_id text NOT NULL CHECK (revision_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  policy_identity text NOT NULL CHECK (policy_identity COLLATE pg_catalog."C" ~ '^worktool-destination-policy-v1:sha256:[0-9a-f]{64}$'),
  key_identity text NOT NULL CHECK (key_identity COLLATE pg_catalog."C" ~ '^worktool-destination-key-v1:sha256:[0-9a-f]{64}$'),
  binding_identity text NOT NULL CHECK (binding_identity COLLATE pg_catalog."C" ~ '^worktool-destination-binding-v1:sha256:[0-9a-f]{64}$'),
  descriptor_bytes bytea NOT NULL CHECK (pg_catalog.octet_length(descriptor_bytes) BETWEEN 1 AND 16384),
  intent_bytes bytea NOT NULL CHECK (pg_catalog.octet_length(intent_bytes) BETWEEN 1 AND 32768),
  PRIMARY KEY (grant_id),
  UNIQUE (authority_id, tenant, operation_id),
  CHECK (issued_at <= not_before AND not_before < expires_at)
);

-- Independent trusted fixture-owner approval slot. This is synthetic control,
-- not a real external issuer, provider, history authority or durable barrier.
CREATE TABLE wt_synthetic_registry.registration_authority_control (
  grant_id text NOT NULL CHECK (grant_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  authority_id text NOT NULL CHECK (authority_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  tenant text NOT NULL CHECK (tenant COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  app text NOT NULL CHECK (app COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  destination_id text NOT NULL CHECK (destination_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  database_name text NOT NULL CHECK (database_name COLLATE pg_catalog."C" ~ '^wt_test_[a-z0-9_]{1,55}$'),
  database_oid oid NOT NULL CHECK (database_oid <> 0),
  schema_oid oid NOT NULL CHECK (schema_oid <> 0),
  environment_id text NOT NULL CHECK (environment_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  deployment_generation text NOT NULL CHECK (deployment_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  recovery_generation text NOT NULL CHECK (recovery_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  routine_definition_sha256 text NOT NULL CHECK (routine_definition_sha256 COLLATE pg_catalog."C" ~ '^[0-9a-f]{64}$'),
  source_manifest_sha256 text NOT NULL CHECK (source_manifest_sha256 COLLATE pg_catalog."C" ~ '^[0-9a-f]{64}$'),
  issuer_role_name text NOT NULL CHECK (issuer_role_name = 'wt_registration_lab_issuer'),
  issuer_role_oid oid NOT NULL CHECK (issuer_role_oid <> 0),
  consumer_role_name text NOT NULL CHECK (consumer_role_name = 'wt_registration_lab_consumer'),
  consumer_role_oid oid NOT NULL CHECK (consumer_role_oid <> 0),
  authority_policy_id text NOT NULL CHECK (authority_policy_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  actor_generation text NOT NULL CHECK (actor_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  approval_generation text NOT NULL CHECK (approval_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  fence_generation text NOT NULL CHECK (fence_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  history_generation text NOT NULL CHECK (history_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  provider_mapping_generation text NOT NULL CHECK (provider_mapping_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  barrier_generation text NOT NULL CHECK (barrier_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  boot_generation text NOT NULL CHECK (boot_generation COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
  control_generation bigint NOT NULL CHECK (control_generation > 0),
  armed boolean NOT NULL,
  revoked boolean NOT NULL,
  clock_healthy boolean NOT NULL,
  decision_not_after timestamptz NOT NULL CHECK (pg_catalog.isfinite(decision_not_after)),
  PRIMARY KEY (grant_id),
  FOREIGN KEY (grant_id) REFERENCES wt_synthetic_registry.registration_prepared_grants (grant_id)
);

CREATE TABLE wt_synthetic_registry.registration_grant_consumptions (
  grant_id text PRIMARY KEY REFERENCES wt_synthetic_registry.registration_prepared_grants (grant_id),
  authority_id text NOT NULL,
  tenant text NOT NULL,
  operation_id text NOT NULL,
  app text NOT NULL,
  destination_id text NOT NULL,
  policy_version bigint NOT NULL,
  key_version bigint NOT NULL,
  binding_identity text NOT NULL,
  intent_bytes bytea NOT NULL,
  control_generation bigint NOT NULL CHECK (control_generation > 0),
  created_xid xid8 NOT NULL,
  recorded_at timestamptz NOT NULL CHECK (pg_catalog.isfinite(recorded_at)),
  outcome text NOT NULL CHECK (outcome = 'REGISTERED_PENDING_NOT_ENABLED'),
  UNIQUE (authority_id, tenant, operation_id),
  UNIQUE (tenant, destination_id),
  FOREIGN KEY (authority_id, tenant, operation_id)
    REFERENCES wt_synthetic_registry.callback_registration_receipts (authority_id, tenant, operation_id),
  FOREIGN KEY (tenant, app, destination_id, policy_version, key_version, binding_identity)
    REFERENCES wt_synthetic_registry.callback_registration_bindings
      (tenant, app, destination_id, policy_version, key_version, binding_identity)
);

CREATE FUNCTION wt_synthetic_registry.reject_registration_authority_mutation()
RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
SET search_path TO pg_catalog, wt_synthetic_registry, pg_temp AS $immutable$
BEGIN
  RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_AUTHORITY_RECORD_IMMUTABLE';
END
$immutable$;

CREATE FUNCTION wt_synthetic_registry.guard_registration_control_update()
RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
SET search_path TO pg_catalog, wt_synthetic_registry, pg_temp AS $control_guard$
BEGIN
  IF (pg_catalog.to_jsonb(NEW) - ARRAY['control_generation','armed','revoked','clock_healthy','decision_not_after'])
       IS DISTINCT FROM
     (pg_catalog.to_jsonb(OLD) - ARRAY['control_generation','armed','revoked','clock_healthy','decision_not_after'])
     OR NEW.control_generation <= OLD.control_generation
     OR (OLD.revoked AND NOT NEW.revoked) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_CONTROL_IMMUTABLE_IDENTITY';
  END IF;
  RETURN NEW;
END
$control_guard$;

CREATE TRIGGER prepared_grant_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON wt_synthetic_registry.registration_prepared_grants
FOR EACH STATEMENT EXECUTE FUNCTION wt_synthetic_registry.reject_registration_authority_mutation();
CREATE TRIGGER consumption_immutable
BEFORE UPDATE OR DELETE OR TRUNCATE ON wt_synthetic_registry.registration_grant_consumptions
FOR EACH STATEMENT EXECUTE FUNCTION wt_synthetic_registry.reject_registration_authority_mutation();
CREATE TRIGGER control_identity_guard
BEFORE UPDATE ON wt_synthetic_registry.registration_authority_control
FOR EACH ROW EXECUTE FUNCTION wt_synthetic_registry.guard_registration_control_update();
CREATE TRIGGER control_retained
BEFORE DELETE OR TRUNCATE ON wt_synthetic_registry.registration_authority_control
FOR EACH STATEMENT EXECUTE FUNCTION wt_synthetic_registry.reject_registration_authority_mutation();

-- This is the only callable mutation surface. No SQL text, actor, tenant,
-- environment, state, bytes or provider input is accepted from the consumer.
CREATE FUNCTION wt_synthetic_registry.consume_prepared_fresh_registration_v1(
  p_grant_id text, p_operation_id text
) RETURNS text
LANGUAGE plpgsql SECURITY DEFINER VOLATILE PARALLEL UNSAFE
SET search_path TO pg_catalog, wt_synthetic_registry, pg_temp
SET row_security TO on
SET lock_timeout TO '1s'
AS $consume$
DECLARE
  g wt_synthetic_registry.registration_prepared_grants%ROWTYPE;
  c wt_synthetic_registry.registration_authority_control%ROWTYPE;
  c_final wt_synthetic_registry.registration_authority_control%ROWTYPE;
  consumed wt_synthetic_registry.registration_grant_consumptions%ROWTYPE;
  d wt_synthetic_registry.callback_destinations%ROWTYPE;
  v wt_synthetic_registry.callback_destination_versions%ROWTYPE;
  policy_row wt_synthetic_registry.callback_registration_policies%ROWTYPE;
  key_row wt_synthetic_registry.callback_registration_keys%ROWTYPE;
  binding_row wt_synthetic_registry.callback_registration_bindings%ROWTYPE;
  receipt_row wt_synthetic_registry.callback_registration_receipts%ROWTYPE;
  tenant_lock text;
  app_active boolean;
  observed boolean := false;
  affected bigint;
  initial_time timestamptz;
  recorded_time timestamptz;
  decision_time timestamptz;
BEGIN
  IF p_grant_id IS NULL OR p_operation_id IS NULL
     OR p_grant_id COLLATE pg_catalog."C" !~ '^[A-Za-z0-9_-]{1,128}$'
     OR p_operation_id COLLATE pg_catalog."C" !~ '^[A-Za-z0-9_-]{1,128}$' THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_INVALID_REFERENCE';
  END IF;
  IF session_user IS DISTINCT FROM 'wt_registration_lab_consumer'
     OR current_user IS DISTINCT FROM 'wt_registration_lab_routine' THEN
    RAISE EXCEPTION USING ERRCODE = '42501', MESSAGE = 'REGISTRATION_CONSUMER_IDENTITY';
  END IF;
  IF pg_catalog.current_database() COLLATE pg_catalog."C" !~ '^wt_test_[a-z0-9_]{1,55}$'
     OR pg_catalog.current_setting('server_version_num') IS DISTINCT FROM '180006'
     OR pg_catalog.current_setting('transaction_isolation') IS DISTINCT FROM 'read committed'
     OR pg_catalog.current_setting('transaction_read_only') IS DISTINCT FROM 'off'
     OR pg_catalog.current_setting('session_replication_role') IS DISTINCT FROM 'origin' THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_TRANSACTION_REQUIRED';
  END IF;
  -- Immutable record only: no mutable authorization decision before locking.
  SELECT x.* INTO g FROM wt_synthetic_registry.registration_prepared_grants AS x
    WHERE x.grant_id = p_grant_id;
  IF NOT FOUND OR g.operation_id IS DISTINCT FROM p_operation_id THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_GRANT_OPERATION_MISMATCH';
  END IF;
  IF session_user IS DISTINCT FROM 'wt_registration_lab_consumer'
     OR current_user IS DISTINCT FROM 'wt_registration_lab_routine'
     OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles AS r
       WHERE r.rolname = session_user AND r.oid = g.consumer_role_oid AND r.rolcanlogin
         AND NOT r.rolsuper AND NOT r.rolcreaterole AND NOT r.rolcreatedb
         AND NOT r.rolreplication AND NOT r.rolbypassrls AND NOT r.rolinherit)
     OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles AS r
       WHERE r.rolname = 'wt_registration_lab_routine' AND NOT r.rolcanlogin
         AND NOT r.rolsuper AND NOT r.rolcreaterole AND NOT r.rolcreatedb
         AND NOT r.rolreplication AND NOT r.rolbypassrls AND NOT r.rolinherit)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members AS m
       JOIN pg_catalog.pg_roles AS r ON r.oid = m.member
       JOIN pg_catalog.pg_roles AS target ON target.oid = m.roleid
       WHERE r.rolname IN ('wt_registration_lab_consumer','wt_registration_lab_issuer',
                          'wt_registration_lab_routine','wt_registration_lab_unrelated')
          OR target.rolname IN ('wt_registration_lab_consumer','wt_registration_lab_issuer',
                               'wt_registration_lab_owner','wt_registration_lab_unrelated')
          OR (target.rolname = 'wt_registration_lab_routine'
              AND (r.rolname <> 'wt_registration_lab_owner' OR m.admin_option OR m.inherit_option OR NOT m.set_option))) THEN
    RAISE EXCEPTION USING ERRCODE = '42501', MESSAGE = 'REGISTRATION_CONSUMER_IDENTITY';
  END IF;
  IF g.database_name IS DISTINCT FROM pg_catalog.current_database()
     OR g.database_oid IS DISTINCT FROM (SELECT d.oid FROM pg_catalog.pg_database AS d
          WHERE d.datname = pg_catalog.current_database())
     OR g.schema_oid IS DISTINCT FROM (SELECT n.oid FROM pg_catalog.pg_namespace AS n
          JOIN pg_catalog.pg_roles AS r ON r.oid = n.nspowner
          WHERE n.nspname = 'wt_synthetic_registry' AND r.rolname = 'wt_registration_lab_owner')
     OR g.issuer_role_oid IS DISTINCT FROM (SELECT r.oid FROM pg_catalog.pg_roles AS r
          WHERE r.rolname = 'wt_registration_lab_issuer' AND r.rolcanlogin
            AND NOT r.rolsuper AND NOT r.rolcreaterole AND NOT r.rolcreatedb
            AND NOT r.rolreplication AND NOT r.rolbypassrls AND NOT r.rolinherit)
     OR g.routine_definition_sha256 IS DISTINCT FROM pg_catalog.encode(pg_catalog.sha256(
          pg_catalog.convert_to(pg_catalog.pg_get_functiondef(
            'wt_synthetic_registry.consume_prepared_fresh_registration_v1(text,text)'::pg_catalog.regprocedure), 'UTF8')), 'hex') THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_ENVIRONMENT_MISMATCH';
  END IF;

  -- Global mutable-lock order: tenant admission row -> exact control slot -> app.
  -- Trusted owner control changes must take this same tenant lock first.
  SELECT x.scope_id INTO tenant_lock FROM wt_synthetic_registry.scope_limits AS x
    WHERE x.scope_kind = 'tenant' AND x.tenant = g.tenant AND x.scope_id = g.tenant
    FOR UPDATE;
  IF NOT FOUND OR tenant_lock IS DISTINCT FROM g.tenant THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_TENANT_LOCK_REQUIRED';
  END IF;
  SELECT x.* INTO c FROM wt_synthetic_registry.registration_authority_control AS x
    WHERE x.grant_id = g.grant_id FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_CONTROL_REQUIRED';
  END IF;
  SELECT x.active INTO app_active FROM wt_synthetic_registry.developer_apps AS x
    WHERE x.tenant = g.tenant AND x.id = g.app FOR SHARE;
  IF NOT FOUND OR app_active IS DISTINCT FROM true THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_APP_INACTIVE';
  END IF;
  initial_time := pg_catalog.clock_timestamp();
  IF ROW(c.grant_id, c.authority_id, c.tenant, c.app, c.destination_id, c.database_name, c.database_oid, c.schema_oid, c.environment_id, c.deployment_generation, c.recovery_generation, c.routine_definition_sha256, c.source_manifest_sha256, c.issuer_role_name, c.issuer_role_oid, c.consumer_role_name, c.consumer_role_oid, c.authority_policy_id, c.actor_generation, c.approval_generation, c.fence_generation, c.history_generation, c.provider_mapping_generation, c.barrier_generation, c.boot_generation)
       IS DISTINCT FROM ROW(g.grant_id, g.authority_id, g.tenant, g.app, g.destination_id, g.database_name, g.database_oid, g.schema_oid, g.environment_id, g.deployment_generation, g.recovery_generation, g.routine_definition_sha256, g.source_manifest_sha256, g.issuer_role_name, g.issuer_role_oid, g.consumer_role_name, g.consumer_role_oid, g.authority_policy_id, g.actor_generation, g.approval_generation, g.fence_generation, g.history_generation, g.provider_mapping_generation, g.barrier_generation, g.boot_generation)
     OR NOT c.armed OR c.revoked OR NOT c.clock_healthy
     OR initial_time < g.issued_at OR initial_time < g.not_before
     OR initial_time >= g.expires_at OR initial_time >= c.decision_not_after THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_AUTHORITY_UNCONFIRMED';
  END IF;

  SELECT x.* INTO consumed FROM wt_synthetic_registry.registration_grant_consumptions AS x
    WHERE x.grant_id = g.grant_id FOR SHARE;
  IF FOUND THEN
    -- A same-transaction row is not evidence of a prior committed registration.
    -- SAVEPOINT/ROLLBACK and SQL function return never establish durability.
    IF consumed.created_xid = pg_catalog.pg_current_xact_id() THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_SAME_TRANSACTION_REPEAT';
    END IF;
    observed := true;
  ELSE
    -- Pair-wide absence, across all apps/states/versions, with bounded malformed
    -- subscription snapshots denying rather than being silently ignored.
    IF EXISTS (SELECT 1 FROM wt_synthetic_registry.scope_limits AS x WHERE x.tenant = g.tenant AND (x.destination_id = g.destination_id OR (x.scope_kind = 'destination' AND x.scope_id = g.destination_id)))
       OR EXISTS (SELECT 1 FROM wt_synthetic_registry.scope_slots AS x WHERE x.tenant = g.tenant AND x.scope_kind = 'destination' AND x.scope_id = g.destination_id)
       OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_subscription_versions AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id)
       OR EXISTS (SELECT 1 FROM wt_synthetic_registry.admission_reservations AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id)
       OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_outbox AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id)
       OR EXISTS (SELECT 1 FROM wt_synthetic_registry.commands AS x WHERE x.tenant = g.tenant AND (x.subscription_snapshot @> pg_catalog.jsonb_build_array(pg_catalog.jsonb_build_object('destinationId', g.destination_id))
        OR pg_catalog.jsonb_typeof(x.subscription_snapshot) IS DISTINCT FROM 'array'
        OR (CASE WHEN pg_catalog.jsonb_typeof(x.subscription_snapshot) = 'array' THEN pg_catalog.jsonb_array_length(x.subscription_snapshot) ELSE 0 END) > 4
        OR pg_catalog.octet_length(x.subscription_snapshot::text) > 16384
        OR ((CASE WHEN pg_catalog.jsonb_typeof(x.subscription_snapshot) = 'array' THEN pg_catalog.jsonb_array_length(x.subscription_snapshot) ELSE 0 END) > 0 AND (pg_catalog.jsonb_typeof(x.subscription_snapshot->0) IS DISTINCT FROM 'object' OR pg_catalog.jsonb_typeof(x.subscription_snapshot->0->'destinationId') IS DISTINCT FROM 'string' OR (x.subscription_snapshot->0->>'destinationId') COLLATE pg_catalog."C" !~ '^[A-Za-z0-9_-]{1,128}$'))
        OR ((CASE WHEN pg_catalog.jsonb_typeof(x.subscription_snapshot) = 'array' THEN pg_catalog.jsonb_array_length(x.subscription_snapshot) ELSE 0 END) > 1 AND (pg_catalog.jsonb_typeof(x.subscription_snapshot->1) IS DISTINCT FROM 'object' OR pg_catalog.jsonb_typeof(x.subscription_snapshot->1->'destinationId') IS DISTINCT FROM 'string' OR (x.subscription_snapshot->1->>'destinationId') COLLATE pg_catalog."C" !~ '^[A-Za-z0-9_-]{1,128}$'))
        OR ((CASE WHEN pg_catalog.jsonb_typeof(x.subscription_snapshot) = 'array' THEN pg_catalog.jsonb_array_length(x.subscription_snapshot) ELSE 0 END) > 2 AND (pg_catalog.jsonb_typeof(x.subscription_snapshot->2) IS DISTINCT FROM 'object' OR pg_catalog.jsonb_typeof(x.subscription_snapshot->2->'destinationId') IS DISTINCT FROM 'string' OR (x.subscription_snapshot->2->>'destinationId') COLLATE pg_catalog."C" !~ '^[A-Za-z0-9_-]{1,128}$'))
        OR ((CASE WHEN pg_catalog.jsonb_typeof(x.subscription_snapshot) = 'array' THEN pg_catalog.jsonb_array_length(x.subscription_snapshot) ELSE 0 END) > 3 AND (pg_catalog.jsonb_typeof(x.subscription_snapshot->3) IS DISTINCT FROM 'object' OR pg_catalog.jsonb_typeof(x.subscription_snapshot->3->'destinationId') IS DISTINCT FROM 'string' OR (x.subscription_snapshot->3->>'destinationId') COLLATE pg_catalog."C" !~ '^[A-Za-z0-9_-]{1,128}$'))))
       OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_destinations AS x WHERE x.tenant = g.tenant AND x.id = g.destination_id)
       OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_destination_versions AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id)
       OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_registration_policies AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id)
       OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_registration_keys AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id)
       OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_registration_bindings AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id)
       OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_registration_receipts AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id)
       OR EXISTS (SELECT 1 FROM wt_synthetic_registry.registration_grant_consumptions AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id) THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_EXISTING_OR_HISTORICAL_IDENTITY';
    END IF;

    -- Five exact business INSERTs. Existing 0009 synchronously creates the exact
    -- version (sixth graph row). No upsert, activation, repair or adoption here.
    INSERT INTO wt_synthetic_registry.callback_destinations
      (tenant, app, id, policy_version, key_version, canonical_url, key_ref, state)
      VALUES (g.tenant, g.app, g.destination_id, g.policy_version, g.key_version,
              g.canonical_url, g.key_ref, 'pending_review');
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected <> 1 THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_GRAPH_UNCONFIRMED';
    END IF;
    INSERT INTO wt_synthetic_registry.callback_registration_policies
      (tenant, app, destination_id, policy_version, policy_identity, canonical_url, cidrs, signature_profile)
      VALUES (g.tenant, g.app, g.destination_id, g.policy_version, g.policy_identity,
              g.canonical_url, g.cidrs, g.signature_profile);
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected <> 1 THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_GRAPH_UNCONFIRMED';
    END IF;
    INSERT INTO wt_synthetic_registry.callback_registration_keys
      (tenant, app, destination_id, key_version, key_identity, key_ref, provider_authority_id, revision_kind, revision_id)
      VALUES (g.tenant, g.app, g.destination_id, g.key_version, g.key_identity, g.key_ref,
              g.provider_authority_id, g.revision_kind, g.revision_id);
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected <> 1 THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_GRAPH_UNCONFIRMED';
    END IF;
    INSERT INTO wt_synthetic_registry.callback_registration_bindings
      (tenant, app, destination_id, policy_version, key_version, policy_identity, key_identity, binding_identity, descriptor_bytes)
      VALUES (g.tenant, g.app, g.destination_id, g.policy_version, g.key_version,
              g.policy_identity, g.key_identity, g.binding_identity, g.descriptor_bytes);
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected <> 1 THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_GRAPH_UNCONFIRMED';
    END IF;
    INSERT INTO wt_synthetic_registry.callback_registration_receipts
      (authority_id, tenant, operation_id, app, destination_id, policy_version, key_version, binding_identity, intent_bytes)
      VALUES (g.authority_id, g.tenant, g.operation_id, g.app, g.destination_id,
              g.policy_version, g.key_version, g.binding_identity, g.intent_bytes);
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected <> 1 THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_GRAPH_UNCONFIRMED';
    END IF;
    recorded_time := pg_catalog.clock_timestamp();
    INSERT INTO wt_synthetic_registry.registration_grant_consumptions
      (grant_id, authority_id, tenant, operation_id, app, destination_id, policy_version, key_version,
       binding_identity, intent_bytes, control_generation, created_xid, recorded_at, outcome)
      VALUES (g.grant_id, g.authority_id, g.tenant, g.operation_id, g.app, g.destination_id,
              g.policy_version, g.key_version, g.binding_identity, g.intent_bytes,
              c.control_generation, pg_catalog.pg_current_xact_id(), recorded_time,
              'REGISTERED_PENDING_NOT_ENABLED');
    GET DIAGNOSTICS affected = ROW_COUNT;
    IF affected <> 1 THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_GRAPH_UNCONFIRMED';
    END IF;
  END IF;

  -- Fresh and observed paths meet here. Compare EVERY stored logical field,
  -- acquire the existing graph locks, reject extras, and never repair a mismatch.
  SELECT x.* INTO d FROM wt_synthetic_registry.callback_destinations AS x
    WHERE x.tenant = g.tenant AND x.id = g.destination_id FOR SHARE;
  IF NOT FOUND OR ROW(d.tenant,d.app,d.id,d.canonical_url,d.policy_version,d.key_ref,d.key_version,d.state,d.enabled)
    IS DISTINCT FROM ROW(g.tenant,g.app,g.destination_id,g.canonical_url,g.policy_version,g.key_ref,g.key_version,'pending_review'::text,false) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_CURRENT_UNCONFIRMED';
  END IF;
  SELECT x.* INTO v FROM wt_synthetic_registry.callback_destination_versions AS x
    WHERE x.tenant = g.tenant AND x.app = g.app AND x.destination_id = g.destination_id
      AND x.policy_version = g.policy_version AND x.key_version = g.key_version FOR SHARE;
  IF NOT FOUND OR ROW(v.tenant,v.app,v.destination_id,v.policy_version,v.key_version,v.canonical_url,v.key_ref)
    IS DISTINCT FROM ROW(g.tenant,g.app,g.destination_id,g.policy_version,g.key_version,g.canonical_url,g.key_ref)
    OR v.revoked_at IS NOT NULL OR NOT pg_catalog.isfinite(v.created_at) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_VERSION_UNCONFIRMED';
  END IF;
  SELECT x.* INTO policy_row FROM wt_synthetic_registry.callback_registration_policies AS x
    WHERE x.tenant = g.tenant AND x.app = g.app AND x.destination_id = g.destination_id
      AND x.policy_version = g.policy_version FOR SHARE;
  IF NOT FOUND OR ROW(policy_row.tenant,policy_row.app,policy_row.destination_id,policy_row.policy_version,
                     policy_row.policy_identity,policy_row.canonical_url,policy_row.cidrs,policy_row.signature_profile)
    IS DISTINCT FROM ROW(g.tenant,g.app,g.destination_id,g.policy_version,g.policy_identity,g.canonical_url,g.cidrs,g.signature_profile) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_POLICY_UNCONFIRMED';
  END IF;
  SELECT x.* INTO key_row FROM wt_synthetic_registry.callback_registration_keys AS x
    WHERE x.tenant = g.tenant AND x.app = g.app AND x.destination_id = g.destination_id
      AND x.key_version = g.key_version FOR SHARE;
  IF NOT FOUND OR ROW(key_row.tenant,key_row.app,key_row.destination_id,key_row.key_version,key_row.key_identity,
                     key_row.key_ref,key_row.provider_authority_id,key_row.revision_kind,key_row.revision_id)
    IS DISTINCT FROM ROW(g.tenant,g.app,g.destination_id,g.key_version,g.key_identity,g.key_ref,g.provider_authority_id,g.revision_kind,g.revision_id) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_KEY_UNCONFIRMED';
  END IF;
  SELECT x.* INTO binding_row FROM wt_synthetic_registry.callback_registration_bindings AS x
    WHERE x.tenant = g.tenant AND x.app = g.app AND x.destination_id = g.destination_id
      AND x.policy_version = g.policy_version AND x.key_version = g.key_version FOR SHARE;
  IF NOT FOUND OR ROW(binding_row.tenant,binding_row.app,binding_row.destination_id,binding_row.policy_version,
                     binding_row.key_version,binding_row.policy_identity,binding_row.key_identity,
                     binding_row.binding_identity,binding_row.descriptor_bytes)
    IS DISTINCT FROM ROW(g.tenant,g.app,g.destination_id,g.policy_version,g.key_version,g.policy_identity,g.key_identity,g.binding_identity,g.descriptor_bytes) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_BINDING_UNCONFIRMED';
  END IF;
  SELECT x.* INTO receipt_row FROM wt_synthetic_registry.callback_registration_receipts AS x
    WHERE x.authority_id = g.authority_id AND x.tenant = g.tenant AND x.operation_id = g.operation_id FOR SHARE;
  IF NOT FOUND OR ROW(receipt_row.authority_id,receipt_row.tenant,receipt_row.operation_id,receipt_row.app,
                     receipt_row.destination_id,receipt_row.policy_version,receipt_row.key_version,
                     receipt_row.binding_identity,receipt_row.intent_bytes)
    IS DISTINCT FROM ROW(g.authority_id,g.tenant,g.operation_id,g.app,g.destination_id,g.policy_version,g.key_version,g.binding_identity,g.intent_bytes) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_RECEIPT_UNCONFIRMED';
  END IF;
  SELECT x.* INTO consumed FROM wt_synthetic_registry.registration_grant_consumptions AS x
    WHERE x.grant_id = g.grant_id FOR SHARE;
  IF NOT FOUND OR ROW(consumed.grant_id,consumed.authority_id,consumed.tenant,consumed.operation_id,consumed.app,
                     consumed.destination_id,consumed.policy_version,consumed.key_version,consumed.binding_identity,
                     consumed.intent_bytes,consumed.control_generation,consumed.outcome)
    IS DISTINCT FROM ROW(g.grant_id,g.authority_id,g.tenant,g.operation_id,g.app,g.destination_id,g.policy_version,
                         g.key_version,g.binding_identity,g.intent_bytes,c.control_generation,'REGISTERED_PENDING_NOT_ENABLED'::text)
    OR NOT pg_catalog.isfinite(consumed.recorded_at) OR consumed.recorded_at < g.not_before
    OR consumed.recorded_at >= g.expires_at OR v.created_at > consumed.recorded_at
    OR v.created_at < g.not_before
    OR (NOT observed AND (consumed.created_xid IS DISTINCT FROM pg_catalog.pg_current_xact_id()
                          OR consumed.recorded_at IS DISTINCT FROM recorded_time))
    OR (observed AND consumed.created_xid = pg_catalog.pg_current_xact_id()) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_CONSUMPTION_UNCONFIRMED';
  END IF;
  IF EXISTS (SELECT 1 FROM wt_synthetic_registry.scope_limits AS x WHERE x.tenant = g.tenant AND (x.destination_id = g.destination_id OR (x.scope_kind = 'destination' AND x.scope_id = g.destination_id)))
     OR EXISTS (SELECT 1 FROM wt_synthetic_registry.scope_slots AS x WHERE x.tenant = g.tenant AND x.scope_kind = 'destination' AND x.scope_id = g.destination_id)
     OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_subscription_versions AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id)
     OR EXISTS (SELECT 1 FROM wt_synthetic_registry.admission_reservations AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id)
     OR EXISTS (SELECT 1 FROM wt_synthetic_registry.callback_outbox AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id)
     OR EXISTS (SELECT 1 FROM wt_synthetic_registry.commands AS x WHERE x.tenant = g.tenant AND (x.subscription_snapshot @> pg_catalog.jsonb_build_array(pg_catalog.jsonb_build_object('destinationId', g.destination_id))
        OR pg_catalog.jsonb_typeof(x.subscription_snapshot) IS DISTINCT FROM 'array'
        OR (CASE WHEN pg_catalog.jsonb_typeof(x.subscription_snapshot) = 'array' THEN pg_catalog.jsonb_array_length(x.subscription_snapshot) ELSE 0 END) > 4
        OR pg_catalog.octet_length(x.subscription_snapshot::text) > 16384
        OR ((CASE WHEN pg_catalog.jsonb_typeof(x.subscription_snapshot) = 'array' THEN pg_catalog.jsonb_array_length(x.subscription_snapshot) ELSE 0 END) > 0 AND (pg_catalog.jsonb_typeof(x.subscription_snapshot->0) IS DISTINCT FROM 'object' OR pg_catalog.jsonb_typeof(x.subscription_snapshot->0->'destinationId') IS DISTINCT FROM 'string' OR (x.subscription_snapshot->0->>'destinationId') COLLATE pg_catalog."C" !~ '^[A-Za-z0-9_-]{1,128}$'))
        OR ((CASE WHEN pg_catalog.jsonb_typeof(x.subscription_snapshot) = 'array' THEN pg_catalog.jsonb_array_length(x.subscription_snapshot) ELSE 0 END) > 1 AND (pg_catalog.jsonb_typeof(x.subscription_snapshot->1) IS DISTINCT FROM 'object' OR pg_catalog.jsonb_typeof(x.subscription_snapshot->1->'destinationId') IS DISTINCT FROM 'string' OR (x.subscription_snapshot->1->>'destinationId') COLLATE pg_catalog."C" !~ '^[A-Za-z0-9_-]{1,128}$'))
        OR ((CASE WHEN pg_catalog.jsonb_typeof(x.subscription_snapshot) = 'array' THEN pg_catalog.jsonb_array_length(x.subscription_snapshot) ELSE 0 END) > 2 AND (pg_catalog.jsonb_typeof(x.subscription_snapshot->2) IS DISTINCT FROM 'object' OR pg_catalog.jsonb_typeof(x.subscription_snapshot->2->'destinationId') IS DISTINCT FROM 'string' OR (x.subscription_snapshot->2->>'destinationId') COLLATE pg_catalog."C" !~ '^[A-Za-z0-9_-]{1,128}$'))
        OR ((CASE WHEN pg_catalog.jsonb_typeof(x.subscription_snapshot) = 'array' THEN pg_catalog.jsonb_array_length(x.subscription_snapshot) ELSE 0 END) > 3 AND (pg_catalog.jsonb_typeof(x.subscription_snapshot->3) IS DISTINCT FROM 'object' OR pg_catalog.jsonb_typeof(x.subscription_snapshot->3->'destinationId') IS DISTINCT FROM 'string' OR (x.subscription_snapshot->3->>'destinationId') COLLATE pg_catalog."C" !~ '^[A-Za-z0-9_-]{1,128}$'))))
     OR (SELECT pg_catalog.count(*) FROM wt_synthetic_registry.callback_destinations AS x WHERE x.tenant = g.tenant AND x.id = g.destination_id) <> 1
     OR (SELECT pg_catalog.count(*) FROM wt_synthetic_registry.callback_destination_versions AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id) <> 1
     OR (SELECT pg_catalog.count(*) FROM wt_synthetic_registry.callback_registration_policies AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id) <> 1
     OR (SELECT pg_catalog.count(*) FROM wt_synthetic_registry.callback_registration_keys AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id) <> 1
     OR (SELECT pg_catalog.count(*) FROM wt_synthetic_registry.callback_registration_bindings AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id) <> 1
     OR (SELECT pg_catalog.count(*) FROM wt_synthetic_registry.callback_registration_receipts AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id) <> 1
     OR (SELECT pg_catalog.count(*) FROM wt_synthetic_registry.registration_grant_consumptions AS x WHERE x.tenant = g.tenant AND x.destination_id = g.destination_id) <> 1 THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_EXTRA_GRAPH_OR_OBLIGATIONS';
  END IF;

  -- Force all deferred work BEFORE the final local authority/clock decision.
  -- This is not an authorization to call external providers or a physical-COMMIT
  -- deadline. The caller must commit immediately in its separately bounded tx.
  SET CONSTRAINTS ALL IMMEDIATE;
  SELECT x.* INTO c_final FROM wt_synthetic_registry.registration_authority_control AS x
    WHERE x.grant_id = g.grant_id;
  IF NOT FOUND OR c_final IS DISTINCT FROM c THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_CONTROL_CHANGED';
  END IF;
  IF session_user IS DISTINCT FROM 'wt_registration_lab_consumer'
     OR current_user IS DISTINCT FROM 'wt_registration_lab_routine'
     OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles AS r
       WHERE r.rolname = session_user AND r.oid = g.consumer_role_oid AND r.rolcanlogin
         AND NOT r.rolsuper AND NOT r.rolcreaterole AND NOT r.rolcreatedb
         AND NOT r.rolreplication AND NOT r.rolbypassrls AND NOT r.rolinherit)
     OR NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles AS r
       WHERE r.rolname = 'wt_registration_lab_routine' AND NOT r.rolcanlogin
         AND NOT r.rolsuper AND NOT r.rolcreaterole AND NOT r.rolcreatedb
         AND NOT r.rolreplication AND NOT r.rolbypassrls AND NOT r.rolinherit)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members AS m
       JOIN pg_catalog.pg_roles AS r ON r.oid = m.member
       JOIN pg_catalog.pg_roles AS target ON target.oid = m.roleid
       WHERE r.rolname IN ('wt_registration_lab_consumer','wt_registration_lab_issuer',
                          'wt_registration_lab_routine','wt_registration_lab_unrelated')
          OR target.rolname IN ('wt_registration_lab_consumer','wt_registration_lab_issuer',
                               'wt_registration_lab_owner','wt_registration_lab_unrelated')
          OR (target.rolname = 'wt_registration_lab_routine'
              AND (r.rolname <> 'wt_registration_lab_owner' OR m.admin_option OR m.inherit_option OR NOT m.set_option))) THEN
    RAISE EXCEPTION USING ERRCODE = '42501', MESSAGE = 'REGISTRATION_CONSUMER_IDENTITY';
  END IF;
  IF g.database_name IS DISTINCT FROM pg_catalog.current_database()
     OR g.database_oid IS DISTINCT FROM (SELECT d.oid FROM pg_catalog.pg_database AS d
          WHERE d.datname = pg_catalog.current_database())
     OR g.schema_oid IS DISTINCT FROM (SELECT n.oid FROM pg_catalog.pg_namespace AS n
          JOIN pg_catalog.pg_roles AS r ON r.oid = n.nspowner
          WHERE n.nspname = 'wt_synthetic_registry' AND r.rolname = 'wt_registration_lab_owner')
     OR g.issuer_role_oid IS DISTINCT FROM (SELECT r.oid FROM pg_catalog.pg_roles AS r
          WHERE r.rolname = 'wt_registration_lab_issuer' AND r.rolcanlogin
            AND NOT r.rolsuper AND NOT r.rolcreaterole AND NOT r.rolcreatedb
            AND NOT r.rolreplication AND NOT r.rolbypassrls AND NOT r.rolinherit)
     OR g.routine_definition_sha256 IS DISTINCT FROM pg_catalog.encode(pg_catalog.sha256(
          pg_catalog.convert_to(pg_catalog.pg_get_functiondef(
            'wt_synthetic_registry.consume_prepared_fresh_registration_v1(text,text)'::pg_catalog.regprocedure), 'UTF8')), 'hex') THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_ENVIRONMENT_MISMATCH';
  END IF;
  decision_time := pg_catalog.clock_timestamp();
  -- FINAL AUTHORITY DECISION: all graph/consumption writes and deferred checks
  -- are already complete. Nothing except bounded result/exception follows.
  IF NOT c_final.armed OR c_final.revoked OR NOT c_final.clock_healthy
     OR decision_time < initial_time OR decision_time < consumed.recorded_at
     OR decision_time < g.not_before OR decision_time >= g.expires_at
     OR decision_time >= c_final.decision_not_after THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_AUTHORITY_UNCONFIRMED';
  END IF;
  IF observed THEN
    RETURN 'OBSERVED_PENDING_NOT_ENABLED';
  END IF;
  RETURN 'REGISTERED_PENDING_NOT_ENABLED';
END
$consume$;

-- Atomic fixture ACL installation. These are real ACL-changing statements if
-- separately executed later. They are inert source in the present candidate.
-- Do not copy them to a persistent service database or grant extra privileges
-- when an integration test reports an unexpected dependency.
REVOKE ALL ON SCHEMA wt_synthetic_registry FROM PUBLIC,
  wt_registration_lab_issuer, wt_registration_lab_consumer,
  wt_registration_lab_routine, wt_registration_lab_unrelated;
REVOKE ALL ON ALL TABLES IN SCHEMA wt_synthetic_registry FROM PUBLIC,
  wt_registration_lab_issuer, wt_registration_lab_consumer,
  wt_registration_lab_routine, wt_registration_lab_unrelated;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA wt_synthetic_registry FROM PUBLIC,
  wt_registration_lab_issuer, wt_registration_lab_consumer,
  wt_registration_lab_routine, wt_registration_lab_unrelated;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA wt_synthetic_registry FROM PUBLIC,
  wt_registration_lab_issuer, wt_registration_lab_consumer,
  wt_registration_lab_routine, wt_registration_lab_unrelated;

-- A relation-level REVOKE does not erase independently granted column ACLs.
-- Refuse those instead of silently broadening/reinterpreting earlier setup.
DO $column_guard$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_attribute AS a
       JOIN pg_catalog.pg_class AS c ON c.oid = a.attrelid
       JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
       CROSS JOIN LATERAL pg_catalog.aclexplode(a.attacl) AS acl
       WHERE n.nspname = 'wt_synthetic_registry' AND acl.grantee <> c.relowner)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_default_acl AS d
       JOIN pg_catalog.pg_roles AS r ON r.oid = d.defaclrole
       CROSS JOIN LATERAL pg_catalog.aclexplode(d.defaclacl) AS acl
       WHERE r.rolname IN ('wt_registration_lab_owner','wt_registration_lab_routine')
         AND acl.grantee <> d.defaclrole) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_UNEXPECTED_COLUMN_OR_DEFAULT_ACL';
  END IF;
END
$column_guard$;

DO $database_acl$
BEGIN
  EXECUTE pg_catalog.format('REVOKE ALL ON DATABASE %I FROM PUBLIC, wt_registration_lab_issuer, wt_registration_lab_consumer, wt_registration_lab_routine, wt_registration_lab_unrelated', pg_catalog.current_database());
  EXECUTE pg_catalog.format('GRANT CONNECT ON DATABASE %I TO wt_registration_lab_issuer, wt_registration_lab_consumer, wt_registration_lab_unrelated', pg_catalog.current_database());
END
$database_acl$;

GRANT USAGE ON SCHEMA wt_synthetic_registry TO
  wt_registration_lab_issuer, wt_registration_lab_consumer,
  wt_registration_lab_routine, wt_registration_lab_unrelated;

-- Required only for PostgreSQL ALTER FUNCTION OWNER. The new owner receives
-- CREATE temporarily INSIDE this transaction; revoke it before commit.
GRANT CREATE ON SCHEMA wt_synthetic_registry TO wt_registration_lab_routine;
ALTER FUNCTION wt_synthetic_registry.consume_prepared_fresh_registration_v1(text,text)
  OWNER TO wt_registration_lab_routine;
REVOKE CREATE ON SCHEMA wt_synthetic_registry FROM wt_registration_lab_routine;
SET LOCAL ROLE wt_registration_lab_routine;
REVOKE ALL ON FUNCTION wt_synthetic_registry.consume_prepared_fresh_registration_v1(text,text)
  FROM PUBLIC, wt_registration_lab_owner, wt_registration_lab_issuer,
       wt_registration_lab_consumer, wt_registration_lab_unrelated;
GRANT EXECUTE ON FUNCTION wt_synthetic_registry.consume_prepared_fresh_registration_v1(text,text)
  TO wt_registration_lab_consumer;
SET LOCAL ROLE NONE;

-- Trusted issuer exception: complete-column INSERT (including equivalent COPY)
-- into ONLY this immutable table. No SELECT/RETURNING, UPDATE, DELETE or consume.
GRANT INSERT (
  grant_id, authority_id, operation_id, tenant, app,
  destination_id, format, evidence_kind, action, expected_revision,
  new_revision, database_name, database_oid, schema_name, schema_oid,
  environment_id, deployment_generation, recovery_generation, routine_definition_sha256, source_manifest_sha256,
  issuer_role_name, issuer_role_oid, consumer_role_name, consumer_role_oid, authority_policy_id,
  actor, actor_generation, approval_ref, approval_digest, approval_generation,
  fence_ref, fence_generation, history_ref, history_digest, history_generation,
  provider_attestation_ref, provider_mapping_generation, barrier_generation, boot_generation, issued_at,
  not_before, expires_at, plan_identity, policy_version, key_version,
  canonical_url, cidrs, signature_profile, key_ref, provider_authority_id,
  revision_kind, revision_id, policy_identity, key_identity, binding_identity,
  descriptor_bytes, intent_bytes
) ON wt_synthetic_registry.registration_prepared_grants TO wt_registration_lab_issuer;

GRANT SELECT ON
  wt_synthetic_registry.registration_prepared_grants,
  wt_synthetic_registry.registration_authority_control,
  wt_synthetic_registry.registration_grant_consumptions,
  wt_synthetic_registry.scope_limits,
  wt_synthetic_registry.scope_slots,
  wt_synthetic_registry.developer_apps,
  wt_synthetic_registry.callback_destinations,
  wt_synthetic_registry.callback_destination_versions,
  wt_synthetic_registry.callback_registration_policies,
  wt_synthetic_registry.callback_registration_keys,
  wt_synthetic_registry.callback_registration_bindings,
  wt_synthetic_registry.callback_registration_receipts,
  wt_synthetic_registry.callback_subscription_versions,
  wt_synthetic_registry.admission_reservations,
  wt_synthetic_registry.callback_outbox,
  wt_synthetic_registry.commands
TO wt_registration_lab_routine;

-- FOR UPDATE/FOR SHARE require UPDATE privilege on at least one table column.
-- These enumerated identity-column grants are lock dependencies, not general
-- UPDATE grants. No login/membership path is available to the consumer/issuer.
GRANT UPDATE (scope_id) ON wt_synthetic_registry.scope_limits TO wt_registration_lab_routine;
GRANT UPDATE (grant_id) ON wt_synthetic_registry.registration_authority_control TO wt_registration_lab_routine;
GRANT UPDATE (id) ON wt_synthetic_registry.developer_apps TO wt_registration_lab_routine;
GRANT UPDATE (id) ON wt_synthetic_registry.callback_destinations TO wt_registration_lab_routine;
GRANT UPDATE (destination_id) ON wt_synthetic_registry.callback_destination_versions TO wt_registration_lab_routine;
GRANT UPDATE (destination_id) ON wt_synthetic_registry.callback_registration_policies TO wt_registration_lab_routine;
GRANT UPDATE (destination_id) ON wt_synthetic_registry.callback_registration_keys TO wt_registration_lab_routine;
GRANT UPDATE (destination_id) ON wt_synthetic_registry.callback_registration_bindings TO wt_registration_lab_routine;
GRANT UPDATE (operation_id) ON wt_synthetic_registry.callback_registration_receipts TO wt_registration_lab_routine;
GRANT UPDATE (grant_id) ON wt_synthetic_registry.registration_grant_consumptions TO wt_registration_lab_routine;

GRANT INSERT (tenant,app,id,policy_version,key_version,canonical_url,key_ref,state)
  ON wt_synthetic_registry.callback_destinations TO wt_registration_lab_routine;
-- 0009 sync trigger inserts revoked_at explicitly, NULL on this pending path.
GRANT INSERT (tenant,app,destination_id,policy_version,key_version,canonical_url,key_ref,revoked_at)
  ON wt_synthetic_registry.callback_destination_versions TO wt_registration_lab_routine;
GRANT INSERT (tenant,app,destination_id,policy_version,policy_identity,canonical_url,cidrs,signature_profile)
  ON wt_synthetic_registry.callback_registration_policies TO wt_registration_lab_routine;
GRANT INSERT (tenant,app,destination_id,key_version,key_identity,key_ref,provider_authority_id,revision_kind,revision_id)
  ON wt_synthetic_registry.callback_registration_keys TO wt_registration_lab_routine;
GRANT INSERT (tenant,app,destination_id,policy_version,key_version,policy_identity,key_identity,binding_identity,descriptor_bytes)
  ON wt_synthetic_registry.callback_registration_bindings TO wt_registration_lab_routine;
GRANT INSERT (authority_id,tenant,operation_id,app,destination_id,policy_version,key_version,binding_identity,intent_bytes)
  ON wt_synthetic_registry.callback_registration_receipts TO wt_registration_lab_routine;
GRANT INSERT (grant_id,authority_id,tenant,operation_id,app,destination_id,policy_version,key_version,
              binding_identity,intent_bytes,control_generation,created_xid,recorded_at,outcome)
  ON wt_synthetic_registry.registration_grant_consumptions TO wt_registration_lab_routine;

GRANT EXECUTE ON FUNCTION
  wt_synthetic_registry.guard_callback_destination_version(),
  wt_synthetic_registry.sync_callback_destination_version(),
  wt_synthetic_registry.guard_callback_registration_policy(),
  wt_synthetic_registry.guard_callback_registration_key(),
  wt_synthetic_registry.guard_callback_registration_binding(),
  wt_synthetic_registry.guard_callback_registration_receipt()
TO wt_registration_lab_routine;

-- No grant options, sequences, CREATE/TEMP, tables owned by routine role, public
-- execute, unlisted helpers or consumer table access may survive installation.
DO $final_acl_audit$
DECLARE
  ns_oid oid := 'wt_synthetic_registry'::pg_catalog.regnamespace;
  routine_oid oid := 'wt_synthetic_registry.consume_prepared_fresh_registration_v1(text,text)'::pg_catalog.regprocedure;
  owner_oid oid;
  routine_owner_oid oid;
  issuer_oid oid;
  consumer_oid oid;
  unrelated_oid oid;
  item record;
BEGIN
  SELECT oid INTO STRICT owner_oid FROM pg_catalog.pg_roles WHERE rolname = 'wt_registration_lab_owner';
  SELECT oid INTO STRICT routine_owner_oid FROM pg_catalog.pg_roles WHERE rolname = 'wt_registration_lab_routine';
  SELECT oid INTO STRICT issuer_oid FROM pg_catalog.pg_roles WHERE rolname = 'wt_registration_lab_issuer';
  SELECT oid INTO STRICT consumer_oid FROM pg_catalog.pg_roles WHERE rolname = 'wt_registration_lab_consumer';
  SELECT oid INTO STRICT unrelated_oid FROM pg_catalog.pg_roles WHERE rolname = 'wt_registration_lab_unrelated';
  IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_proc AS p
       WHERE p.oid = routine_oid AND p.proowner = routine_owner_oid AND p.prosecdef
         AND p.prokind = 'f' AND p.provolatile = 'v' AND p.proparallel = 'u'
         AND 'search_path=pg_catalog, wt_synthetic_registry, pg_temp' = ANY(p.proconfig))
     OR (SELECT pg_catalog.count(*) FROM pg_catalog.pg_proc AS p
         WHERE p.pronamespace = ns_oid AND p.proname = 'consume_prepared_fresh_registration_v1') <> 1
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_class AS c WHERE c.relnamespace = ns_oid AND c.relowner <> owner_oid)
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_proc AS p WHERE p.pronamespace = ns_oid
         AND p.oid <> routine_oid AND (p.prosecdef OR p.proowner <> owner_oid)) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_FINAL_OWNERSHIP';
  END IF;
  FOR item IN SELECT r.oid FROM pg_catalog.pg_roles AS r WHERE r.oid IN
       (issuer_oid,consumer_oid,routine_owner_oid,unrelated_oid) LOOP
    IF pg_catalog.has_database_privilege(item.oid, pg_catalog.current_database(), 'CREATE')
       OR pg_catalog.has_database_privilege(item.oid, pg_catalog.current_database(), 'TEMP')
       OR pg_catalog.has_schema_privilege(item.oid, ns_oid, 'CREATE') THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_FINAL_DDL_PRIVILEGE';
    END IF;
  END LOOP;
  -- Effective column checks matter: has_table_privilege alone misses them.
  FOR item IN SELECT c.oid, c.relname, a.attnum FROM pg_catalog.pg_class AS c
       JOIN pg_catalog.pg_attribute AS a ON a.attrelid = c.oid
       WHERE c.relnamespace = ns_oid AND c.relkind = 'r' AND a.attnum > 0 AND NOT a.attisdropped LOOP
    IF pg_catalog.has_column_privilege(consumer_oid,item.oid,item.attnum,'SELECT,INSERT,UPDATE,REFERENCES')
       OR pg_catalog.has_column_privilege(unrelated_oid,item.oid,item.attnum,'SELECT,INSERT,UPDATE,REFERENCES')
       OR pg_catalog.has_column_privilege(issuer_oid,item.oid,item.attnum,'SELECT,UPDATE,REFERENCES')
       OR (item.relname <> 'registration_prepared_grants'
           AND pg_catalog.has_column_privilege(issuer_oid,item.oid,item.attnum,'INSERT')) THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_FINAL_COLUMN_PRIVILEGE';
    END IF;
  END LOOP;
  FOR item IN SELECT c.oid FROM pg_catalog.pg_class AS c WHERE c.relnamespace = ns_oid AND c.relkind = 'r' LOOP
    IF pg_catalog.has_table_privilege(consumer_oid,item.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
       OR pg_catalog.has_table_privilege(unrelated_oid,item.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
       OR pg_catalog.has_table_privilege(issuer_oid,item.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
       OR pg_catalog.has_table_privilege(routine_owner_oid,item.oid,'UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER') THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_FINAL_TABLE_PRIVILEGE';
    END IF;
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_proc AS p
       CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(p.proacl,pg_catalog.acldefault('f',p.proowner))) AS a
       WHERE p.pronamespace = ns_oid AND (a.grantee = 0 OR a.is_grantable AND a.grantee <> p.proowner
         OR a.grantee NOT IN (owner_oid,routine_owner_oid,consumer_oid)
         OR a.grantee = consumer_oid AND p.oid <> routine_oid))
     OR EXISTS (SELECT 1 FROM pg_catalog.pg_class AS c
       CROSS JOIN LATERAL pg_catalog.aclexplode(COALESCE(c.relacl,pg_catalog.acldefault(CASE WHEN c.relkind = 'S' THEN 's'::"char" ELSE 'r'::"char" END,c.relowner))) AS a
       WHERE c.relnamespace = ns_oid AND c.relkind IN ('r','S')
         AND (a.grantee NOT IN (owner_oid,routine_owner_oid) OR a.is_grantable AND a.grantee <> c.relowner)) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_FINAL_ACL_LEAK';
  END IF;
  -- Exact reachable graph trigger inventory: no hidden trigger callbacks and all
  -- existing invoker routines resolve pg_catalog first, fixed schema, temp last.
  IF EXISTS (
    WITH expected(rel, name, fn, kind) AS (VALUES
      ('callback_destinations','destination_current_guard','guard_callback_destination_current',27),
      ('callback_destinations','destination_version_sync','sync_callback_destination_version',21),
      ('callback_destination_versions','destination_version_guard','guard_callback_destination_version',31),
      ('callback_registration_policies','registration_policy_insert_guard','guard_callback_registration_policy',7),
      ('callback_registration_policies','registration_immutable','reject_callback_registration_mutation',58),
      ('callback_registration_keys','registration_key_insert_guard','guard_callback_registration_key',7),
      ('callback_registration_keys','registration_immutable','reject_callback_registration_mutation',58),
      ('callback_registration_bindings','registration_binding_insert_guard','guard_callback_registration_binding',7),
      ('callback_registration_bindings','registration_immutable','reject_callback_registration_mutation',58),
      ('callback_registration_receipts','registration_receipt_insert_guard','guard_callback_registration_receipt',7),
      ('callback_registration_receipts','registration_immutable','reject_callback_registration_mutation',58),
      ('registration_prepared_grants','prepared_grant_immutable','reject_registration_authority_mutation',58),
      ('registration_grant_consumptions','consumption_immutable','reject_registration_authority_mutation',58),
      ('registration_authority_control','control_identity_guard','guard_registration_control_update',19),
      ('registration_authority_control','control_retained','reject_registration_authority_mutation',42)),
    actual AS (SELECT c.relname::text AS rel,t.tgname::text AS name,p.proname::text AS fn,
                  t.tgenabled,t.tgtype::integer AS kind,t.tgnargs,t.tgqual,t.tgattr,t.tgconstraint,
                  p.prosecdef,p.proowner,p.pronamespace,p.proconfig
      FROM pg_catalog.pg_trigger AS t JOIN pg_catalog.pg_class AS c ON c.oid=t.tgrelid
      JOIN pg_catalog.pg_proc AS p ON p.oid=t.tgfoid
      WHERE c.relnamespace=ns_oid AND NOT t.tgisinternal
        AND c.relname IN ('callback_destinations','callback_destination_versions',
          'callback_registration_policies','callback_registration_keys','callback_registration_bindings',
          'callback_registration_receipts','registration_prepared_grants',
          'registration_grant_consumptions','registration_authority_control'))
    SELECT 1 FROM expected AS e FULL JOIN actual AS a USING (rel,name,fn,kind)
      WHERE e.rel IS NULL OR a.rel IS NULL OR a.tgenabled <> 'O' OR a.prosecdef
        OR a.proowner <> owner_oid OR a.pronamespace <> ns_oid
        OR a.tgnargs <> 0 OR a.tgqual IS NOT NULL OR a.tgattr <> ''::pg_catalog.int2vector OR a.tgconstraint <> 0
        OR NOT COALESCE('search_path=pg_catalog, wt_synthetic_registry, pg_temp' = ANY(a.proconfig),false)
  ) THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_LAB_TRIGGER_INVENTORY';
  END IF;
END
$final_acl_audit$;
COMMIT;
