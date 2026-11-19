-- LOCAL, UNEXECUTED STORAGE CANDIDATE. This is testdata, NOT a numbered migration.
-- No installer, startup hook, role/permission change, or backfill accompanies it.
-- A future, separately reviewed isolated-schema proof may execute this text in
-- its bounded transaction after validating the unchanged 0002/0009/0010 schema.
-- Existing destinations may remain entirely unbound. No existing row is changed.
--
-- These constraints are NOT an authenticated registrar or a security boundary.
-- They do not prove historical freshness, authority, canonical descriptor/hash
-- equality, provider mapping, or the meaning/authenticity of intent_bytes. Those
-- need the reviewed descriptor, transaction, live verifier, and real PG evidence.
-- Existing broad writers can still create/activate current destinations; nothing
-- here grants, revokes, replaces, or claims to constrain their existing powers.

DO $candidate$
DECLARE
  ns text := pg_catalog.current_schema();
  ns_oid oid;
  ns_owner oid;
  relation_name text;
  object_name text;
BEGIN
  -- Capture the target before any function's pg_catalog-first path is defined.
  -- Do not change the invoking connection's search_path. All generated relation,
  -- function and trigger references are quoted and qualified with this one ns.
  SELECT n.oid, n.nspowner INTO ns_oid, ns_owner
    FROM pg_catalog.pg_namespace AS n WHERE n.nspname = ns;
  IF ns IS DISTINCT FROM 'wt_synthetic_registry' OR ns_oid IS NULL
     OR ns IN ('public', 'pg_catalog', 'information_schema')
     OR pg_catalog.left(ns, 3) = 'pg_'
     OR ns COLLATE pg_catalog."C" !~ '^[A-Za-z_][A-Za-z0-9_]{0,62}$'
     OR NOT EXISTS (
       SELECT 1 FROM pg_catalog.pg_roles AS r
        WHERE r.oid = ns_owner AND r.rolname = current_user)
     OR EXISTS (
       SELECT 1 FROM pg_catalog.pg_namespace AS n,
         LATERAL pg_catalog.aclexplode(
           COALESCE(n.nspacl, pg_catalog.acldefault('n', n.nspowner))) AS a
        WHERE n.oid = ns_oid AND a.privilege_type = 'CREATE'
          AND a.grantee <> ns_owner) THEN
    RAISE EXCEPTION USING ERRCODE = '23514',
      MESSAGE = 'REGISTRATION_CANDIDATE_SCHEMA_GUARD';
  END IF;

  -- These are only structural prerequisites, not a migration/provenance audit.
  FOREACH relation_name IN ARRAY ARRAY[
    'developer_apps', 'callback_destinations',
    'callback_destination_versions', 'callback_replay_receipts'] LOOP
    IF NOT EXISTS (
      SELECT 1 FROM pg_catalog.pg_class AS c
       WHERE c.relnamespace = ns_oid AND c.relname = relation_name
         AND c.relkind = 'r' AND c.relpersistence = 'p') THEN
      RAISE EXCEPTION USING ERRCODE = '23514',
        MESSAGE = 'REGISTRATION_CANDIDATE_BASELINE_REQUIRED';
    END IF;
  END LOOP;

  -- Fail closed instead of adopting, replacing, or repairing an earlier object.
  FOREACH object_name IN ARRAY ARRAY[
    'callback_registration_policies', 'callback_registration_keys',
    'callback_registration_bindings', 'callback_registration_receipts'] LOOP
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_class AS c
                WHERE c.relnamespace = ns_oid AND c.relname = object_name) THEN
      RAISE EXCEPTION USING ERRCODE = '23514',
        MESSAGE = 'REGISTRATION_CANDIDATE_OBJECT_EXISTS';
    END IF;
  END LOOP;
  FOREACH object_name IN ARRAY ARRAY[
    'reject_callback_registration_mutation',
    'guard_callback_registration_policy', 'guard_callback_registration_key',
    'guard_callback_registration_binding', 'guard_callback_registration_receipt'] LOOP
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_proc AS p
                WHERE p.pronamespace = ns_oid AND p.proname = object_name) THEN
      RAISE EXCEPTION USING ERRCODE = '23514',
        MESSAGE = 'REGISTRATION_CANDIDATE_OBJECT_EXISTS';
    END IF;
  END LOOP;

  EXECUTE pg_catalog.format($ddl$
    CREATE TABLE %1$I.callback_registration_policies (
      tenant text NOT NULL CHECK (tenant COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      app text NOT NULL CHECK (app COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      destination_id text NOT NULL CHECK (destination_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      policy_version bigint NOT NULL CHECK (policy_version > 0),
      policy_identity text NOT NULL CHECK (policy_identity COLLATE pg_catalog."C" ~ '^worktool-destination-policy-v1:sha256:[0-9a-f]{64}$'),
      canonical_url text NOT NULL CHECK (pg_catalog.octet_length(canonical_url) BETWEEN 1 AND 2048),
      cidrs text[] NOT NULL CHECK (
        pg_catalog.array_ndims(cidrs) = 1 AND pg_catalog.array_lower(cidrs, 1) = 1
        AND pg_catalog.cardinality(cidrs) BETWEEN 1 AND 32
        AND pg_catalog.array_position(cidrs, NULL::text) IS NULL),
      signature_profile text NOT NULL CHECK (signature_profile = 'worktool-callback-hmac-sha256-v1'),
      PRIMARY KEY (tenant, app, destination_id, policy_version),
      UNIQUE (tenant, app, destination_id, policy_version, policy_identity),
      FOREIGN KEY (tenant, app, destination_id)
        REFERENCES %1$I.callback_destinations (tenant, app, id)
    )
  $ddl$, ns);

  EXECUTE pg_catalog.format($ddl$
    CREATE TABLE %1$I.callback_registration_keys (
      tenant text NOT NULL CHECK (tenant COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      app text NOT NULL CHECK (app COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      destination_id text NOT NULL CHECK (destination_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      key_version bigint NOT NULL CHECK (key_version > 0),
      key_identity text NOT NULL CHECK (key_identity COLLATE pg_catalog."C" ~ '^worktool-destination-key-v1:sha256:[0-9a-f]{64}$'),
      key_ref text NOT NULL CHECK (key_ref COLLATE pg_catalog."C" ~ '^[!-~][!-~]{0,255}$'),
      provider_authority_id text NOT NULL CHECK (provider_authority_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      revision_kind text NOT NULL CHECK (revision_kind = 'exact'),
      revision_id text NOT NULL CHECK (revision_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      PRIMARY KEY (tenant, app, destination_id, key_version),
      UNIQUE (tenant, app, destination_id, key_version, key_identity),
      FOREIGN KEY (tenant, app, destination_id)
        REFERENCES %1$I.callback_destinations (tenant, app, id)
    )
  $ddl$, ns);

  EXECUTE pg_catalog.format($ddl$
    CREATE TABLE %1$I.callback_registration_bindings (
      tenant text NOT NULL CHECK (tenant COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      app text NOT NULL CHECK (app COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      destination_id text NOT NULL CHECK (destination_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      policy_version bigint NOT NULL CHECK (policy_version > 0),
      key_version bigint NOT NULL CHECK (key_version > 0),
      policy_identity text NOT NULL CHECK (policy_identity COLLATE pg_catalog."C" ~ '^worktool-destination-policy-v1:sha256:[0-9a-f]{64}$'),
      key_identity text NOT NULL CHECK (key_identity COLLATE pg_catalog."C" ~ '^worktool-destination-key-v1:sha256:[0-9a-f]{64}$'),
      binding_identity text NOT NULL CHECK (binding_identity COLLATE pg_catalog."C" ~ '^worktool-destination-binding-v1:sha256:[0-9a-f]{64}$'),
      descriptor_bytes bytea NOT NULL CHECK (pg_catalog.octet_length(descriptor_bytes) BETWEEN 1 AND 16384),
      PRIMARY KEY (tenant, app, destination_id, policy_version, key_version),
      UNIQUE (tenant, app, destination_id, policy_version, key_version, binding_identity),
      FOREIGN KEY (tenant, app, destination_id, policy_version, policy_identity)
        REFERENCES %1$I.callback_registration_policies
          (tenant, app, destination_id, policy_version, policy_identity),
      FOREIGN KEY (tenant, app, destination_id, key_version, key_identity)
        REFERENCES %1$I.callback_registration_keys
          (tenant, app, destination_id, key_version, key_identity),
      FOREIGN KEY (tenant, app, destination_id, policy_version, key_version)
        REFERENCES %1$I.callback_destination_versions
          (tenant, app, destination_id, policy_version, key_version)
    )
  $ddl$, ns);

  EXECUTE pg_catalog.format($ddl$
    CREATE TABLE %1$I.callback_registration_receipts (
      authority_id text NOT NULL CHECK (authority_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      tenant text NOT NULL CHECK (tenant COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      operation_id text NOT NULL CHECK (operation_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      app text NOT NULL CHECK (app COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      destination_id text NOT NULL CHECK (destination_id COLLATE pg_catalog."C" ~ '^[A-Za-z0-9_-]{1,128}$'),
      policy_version bigint NOT NULL CHECK (policy_version > 0),
      key_version bigint NOT NULL CHECK (key_version > 0),
      binding_identity text NOT NULL CHECK (binding_identity COLLATE pg_catalog."C" ~ '^worktool-destination-binding-v1:sha256:[0-9a-f]{64}$'),
      intent_bytes bytea NOT NULL CHECK (pg_catalog.octet_length(intent_bytes) BETWEEN 1 AND 32768),
      PRIMARY KEY (authority_id, tenant, operation_id),
      UNIQUE (tenant, destination_id),
      FOREIGN KEY (tenant, app, destination_id, policy_version, key_version, binding_identity)
        REFERENCES %1$I.callback_registration_bindings
          (tenant, app, destination_id, policy_version, key_version, binding_identity)
    )
  $ddl$, ns);

  -- Statement-level guards also reject no-op and zero-row UPDATE/DELETE, and
  -- TRUNCATE. These are not protection against owners disabling/droping guards.
  EXECUTE pg_catalog.format($ddl$
    CREATE FUNCTION %1$I.reject_callback_registration_mutation()
    RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
    SET search_path TO pg_catalog, %1$I, pg_temp AS $fn$
    BEGIN
      RAISE EXCEPTION USING ERRCODE = '23514',
        MESSAGE = 'REGISTRATION_RECORD_IMMUTABLE';
    END
    $fn$
  $ddl$, ns);

  EXECUTE pg_catalog.format($ddl$
    CREATE FUNCTION %1$I.guard_callback_registration_policy()
    RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
    SET search_path TO pg_catalog, %1$I, pg_temp AS $fn$
    DECLARE
      current_row %1$I.callback_destinations%%ROWTYPE;
      cidr text;
      previous_cidr text;
    BEGIN
      -- Bound count BEFORE iteration; CHECK constraints run after BEFORE triggers.
      IF NEW.cidrs IS NULL OR pg_catalog.array_ndims(NEW.cidrs) IS DISTINCT FROM 1
         OR pg_catalog.array_lower(NEW.cidrs, 1) IS DISTINCT FROM 1
         OR pg_catalog.cardinality(NEW.cidrs) NOT BETWEEN 1 AND 32 THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_CIDR_BOUNDS';
      END IF;
      FOREACH cidr IN ARRAY NEW.cidrs LOOP
        IF cidr IS NULL OR pg_catalog.octet_length(cidr) NOT BETWEEN 3 AND 43
           OR cidr COLLATE pg_catalog."C" !~ '^[0-9a-f:.]+/[0-9]{1,3}$'
           OR (previous_cidr IS NOT NULL AND
               previous_cidr COLLATE pg_catalog."C" >= cidr COLLATE pg_catalog."C") THEN
          RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_CIDR_BOUNDS';
        END IF;
        previous_cidr := cidr;
      END LOOP;
      -- CIDR semantics/public ranges and URL canonicalization remain Go checks.
      SELECT d.* INTO current_row FROM %1$I.callback_destinations AS d
       WHERE d.tenant = NEW.tenant AND d.app = NEW.app AND d.id = NEW.destination_id
       FOR SHARE;
      IF NOT FOUND OR current_row.state IS DISTINCT FROM 'pending_review'
         OR current_row.policy_version IS DISTINCT FROM NEW.policy_version
         OR current_row.canonical_url IS DISTINCT FROM NEW.canonical_url THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_POLICY_NOT_CURRENT_PENDING';
      END IF;
      PERFORM 1 FROM %1$I.callback_destination_versions AS v
       WHERE v.tenant = NEW.tenant AND v.app = NEW.app AND v.destination_id = NEW.destination_id
         AND v.policy_version = NEW.policy_version AND v.key_version = current_row.key_version
         AND v.canonical_url = NEW.canonical_url AND v.key_ref = current_row.key_ref
         AND v.revoked_at IS NULL FOR SHARE;
      IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_POLICY_VERSION_MISMATCH';
      END IF;
      RETURN NEW;
    END
    $fn$
  $ddl$, ns);

  EXECUTE pg_catalog.format($ddl$
    CREATE FUNCTION %1$I.guard_callback_registration_key()
    RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
    SET search_path TO pg_catalog, %1$I, pg_temp AS $fn$
    DECLARE current_row %1$I.callback_destinations%%ROWTYPE;
    BEGIN
      SELECT d.* INTO current_row FROM %1$I.callback_destinations AS d
       WHERE d.tenant = NEW.tenant AND d.app = NEW.app AND d.id = NEW.destination_id
       FOR SHARE;
      IF NOT FOUND OR current_row.state IS DISTINCT FROM 'pending_review'
         OR current_row.key_version IS DISTINCT FROM NEW.key_version
         OR current_row.key_ref IS DISTINCT FROM NEW.key_ref THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_KEY_NOT_CURRENT_PENDING';
      END IF;
      PERFORM 1 FROM %1$I.callback_destination_versions AS v
       WHERE v.tenant = NEW.tenant AND v.app = NEW.app AND v.destination_id = NEW.destination_id
         AND v.policy_version = current_row.policy_version AND v.key_version = NEW.key_version
         AND v.canonical_url = current_row.canonical_url AND v.key_ref = NEW.key_ref
         AND v.revoked_at IS NULL FOR SHARE;
      IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_KEY_VERSION_MISMATCH';
      END IF;
      RETURN NEW;
    END
    $fn$
  $ddl$, ns);

  EXECUTE pg_catalog.format($ddl$
    CREATE FUNCTION %1$I.guard_callback_registration_binding()
    RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
    SET search_path TO pg_catalog, %1$I, pg_temp AS $fn$
    DECLARE current_row %1$I.callback_destinations%%ROWTYPE;
    BEGIN
      SELECT d.* INTO current_row FROM %1$I.callback_destinations AS d
       WHERE d.tenant = NEW.tenant AND d.app = NEW.app AND d.id = NEW.destination_id
       FOR SHARE;
      IF NOT FOUND OR current_row.state IS DISTINCT FROM 'pending_review'
         OR current_row.policy_version IS DISTINCT FROM NEW.policy_version
         OR current_row.key_version IS DISTINCT FROM NEW.key_version THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_BINDING_NOT_CURRENT_PENDING';
      END IF;
      PERFORM 1 FROM %1$I.callback_destination_versions AS v
        JOIN %1$I.callback_registration_policies AS p
          ON (p.tenant, p.app, p.destination_id, p.policy_version, p.policy_identity)
           = (NEW.tenant, NEW.app, NEW.destination_id, NEW.policy_version, NEW.policy_identity)
        JOIN %1$I.callback_registration_keys AS k
          ON (k.tenant, k.app, k.destination_id, k.key_version, k.key_identity)
           = (NEW.tenant, NEW.app, NEW.destination_id, NEW.key_version, NEW.key_identity)
       WHERE (v.tenant, v.app, v.destination_id, v.policy_version, v.key_version)
           = (NEW.tenant, NEW.app, NEW.destination_id, NEW.policy_version, NEW.key_version)
         AND v.revoked_at IS NULL
         AND v.canonical_url = current_row.canonical_url AND v.canonical_url = p.canonical_url
         AND v.key_ref = current_row.key_ref AND v.key_ref = k.key_ref
       FOR SHARE OF v;
      IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_BINDING_CONTENT_MISMATCH';
      END IF;
      RETURN NEW;
    END
    $fn$
  $ddl$, ns);

  EXECUTE pg_catalog.format($ddl$
    CREATE FUNCTION %1$I.guard_callback_registration_receipt()
    RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER
    SET search_path TO pg_catalog, %1$I, pg_temp AS $fn$
    DECLARE current_row %1$I.callback_destinations%%ROWTYPE;
    BEGIN
      SELECT d.* INTO current_row FROM %1$I.callback_destinations AS d
       WHERE d.tenant = NEW.tenant AND d.app = NEW.app AND d.id = NEW.destination_id
       FOR SHARE;
      IF NOT FOUND OR current_row.state IS DISTINCT FROM 'pending_review'
         OR current_row.policy_version IS DISTINCT FROM NEW.policy_version
         OR current_row.key_version IS DISTINCT FROM NEW.key_version THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_RECEIPT_NOT_CURRENT_PENDING';
      END IF;
      PERFORM 1 FROM %1$I.callback_destination_versions AS v
       WHERE (v.tenant, v.app, v.destination_id, v.policy_version, v.key_version)
           = (NEW.tenant, NEW.app, NEW.destination_id, NEW.policy_version, NEW.key_version)
         AND v.canonical_url = current_row.canonical_url AND v.key_ref = current_row.key_ref
         AND v.revoked_at IS NULL FOR SHARE;
      IF NOT FOUND THEN
        RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'REGISTRATION_RECEIPT_VERSION_MISMATCH';
      END IF;
      -- The FK below insertion binds the exact immutable binding identity. The
      -- opaque bytes must also bind actor, target, approval, fence, expected/new
      -- revisions, plan audit identity, and full descriptors in the caller.
      RETURN NEW;
    END
    $fn$
  $ddl$, ns);

  EXECUTE pg_catalog.format($ddl$
    CREATE TRIGGER registration_policy_insert_guard
    BEFORE INSERT ON %1$I.callback_registration_policies
    FOR EACH ROW EXECUTE FUNCTION %1$I.guard_callback_registration_policy()
  $ddl$, ns);
  EXECUTE pg_catalog.format($ddl$
    CREATE TRIGGER registration_key_insert_guard
    BEFORE INSERT ON %1$I.callback_registration_keys
    FOR EACH ROW EXECUTE FUNCTION %1$I.guard_callback_registration_key()
  $ddl$, ns);
  EXECUTE pg_catalog.format($ddl$
    CREATE TRIGGER registration_binding_insert_guard
    BEFORE INSERT ON %1$I.callback_registration_bindings
    FOR EACH ROW EXECUTE FUNCTION %1$I.guard_callback_registration_binding()
  $ddl$, ns);
  EXECUTE pg_catalog.format($ddl$
    CREATE TRIGGER registration_receipt_insert_guard
    BEFORE INSERT ON %1$I.callback_registration_receipts
    FOR EACH ROW EXECUTE FUNCTION %1$I.guard_callback_registration_receipt()
  $ddl$, ns);

  FOREACH relation_name IN ARRAY ARRAY[
    'callback_registration_policies', 'callback_registration_keys',
    'callback_registration_bindings', 'callback_registration_receipts'] LOOP
    EXECUTE pg_catalog.format(
      'CREATE TRIGGER registration_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON %I.%I '
      'FOR EACH STATEMENT EXECUTE FUNCTION %I.reject_callback_registration_mutation()',
      ns, relation_name, ns);
  END LOOP;
END
$candidate$;
