-- Phase A candidate. Apply in one transaction, before callback history exists.
-- Never manufacture historical URL/key identity from mutable legacy config.
SET LOCAL lock_timeout='1s';
SET LOCAL statement_timeout='5s';
LOCK TABLE callback_destinations, callback_subscription_versions, admission_reservations,
 callback_outbox, developer_callbacks, commands IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF current_schema() IN ('public','pg_catalog','information_schema') THEN
  RAISE EXCEPTION 'CALLBACK_VERSION_SCHEMA_GUARD';
 END IF;
 IF EXISTS(SELECT 1 FROM callback_subscription_versions)
 OR EXISTS(SELECT 1 FROM admission_reservations WHERE kind='callback')
 OR EXISTS(SELECT 1 FROM callback_outbox)
 OR EXISTS(SELECT 1 FROM developer_callbacks)
 OR EXISTS(SELECT 1 FROM commands WHERE subscription_snapshot<>'[]'::jsonb) THEN
  RAISE EXCEPTION 'CALLBACK_LEGACY_HISTORY_REQUIRES_RECONCILIATION';
 END IF;
END $$;

CREATE TABLE callback_destination_versions (
 tenant text NOT NULL, app text NOT NULL, destination_id text NOT NULL,
 policy_version bigint NOT NULL CHECK(policy_version>0),
 key_version bigint NOT NULL CHECK(key_version>0),
 canonical_url text NOT NULL, key_ref text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), revoked_at timestamptz,
 PRIMARY KEY(tenant,app,destination_id,policy_version,key_version),
 FOREIGN KEY(tenant,app,destination_id) REFERENCES callback_destinations(tenant,app,id)
);
-- Only destination-only config can reach this point. No history is inferred.
INSERT INTO callback_destination_versions(tenant,app,destination_id,policy_version,key_version,canonical_url,key_ref,revoked_at)
 SELECT tenant,app,id,policy_version,key_version,canonical_url,key_ref,
 CASE WHEN state='revoked' THEN clock_timestamp() END FROM callback_destinations;
ALTER TABLE callback_destinations ADD CONSTRAINT destination_current_version FOREIGN KEY
 (tenant,app,id,policy_version,key_version)
 REFERENCES callback_destination_versions(tenant,app,destination_id,policy_version,key_version)
 DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE callback_subscription_versions ADD CONSTRAINT subscription_exact_destination_version FOREIGN KEY
 (tenant,app,destination_id,destination_policy_version,key_version)
 REFERENCES callback_destination_versions(tenant,app,destination_id,policy_version,key_version);

CREATE FUNCTION guard_callback_destination_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DESTINATION_VERSION_HISTORY_RETAINED';
 END IF;
 IF TG_OP='INSERT' THEN
  -- Compatibility writes create versions only from their exact current row.
  -- A caller cannot append a detached future version or a different app/key.
  PERFORM 1 FROM callback_destinations d WHERE d.tenant=NEW.tenant AND d.app=NEW.app
   AND d.id=NEW.destination_id AND d.policy_version=NEW.policy_version AND d.key_version=NEW.key_version
   AND d.canonical_url=NEW.canonical_url AND d.key_ref=NEW.key_ref
   AND (d.state<>'revoked' OR NEW.revoked_at IS NOT NULL) FOR SHARE;
  IF NOT FOUND THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DESTINATION_VERSION_NOT_CURRENT';
  END IF;
  RETURN NEW;
 END IF;
 IF (to_jsonb(NEW)-'revoked_at') IS DISTINCT FROM (to_jsonb(OLD)-'revoked_at')
 OR (OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMMUTABLE_DESTINATION_VERSION';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER destination_version_guard BEFORE INSERT OR UPDATE OR DELETE ON callback_destination_versions
 FOR EACH ROW EXECUTE FUNCTION guard_callback_destination_version();

CREATE FUNCTION guard_callback_destination_current() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DESTINATION_IDENTITY_RETAINED';
 END IF;
 IF ROW(NEW.tenant,NEW.app,NEW.id) IS DISTINCT FROM ROW(OLD.tenant,OLD.app,OLD.id) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMMUTABLE_DESTINATION_IDENTITY';
 END IF;
 IF OLD.state='revoked' AND ROW(NEW.canonical_url,NEW.policy_version,NEW.key_ref,NEW.key_version,NEW.state)
 IS DISTINCT FROM ROW(OLD.canonical_url,OLD.policy_version,OLD.key_ref,OLD.key_version,OLD.state) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DESTINATION_REVOCATION_TERMINAL';
 END IF;
 IF NEW.policy_version<OLD.policy_version OR NEW.key_version<OLD.key_version
 OR (NEW.canonical_url IS DISTINCT FROM OLD.canonical_url AND NEW.policy_version<=OLD.policy_version)
 OR (NEW.key_ref IS DISTINCT FROM OLD.key_ref AND NEW.key_version<=OLD.key_version) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DESTINATION_ROTATION_REQUIRES_NEW_VERSION';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER destination_current_guard BEFORE UPDATE OR DELETE ON callback_destinations
 FOR EACH ROW EXECUTE FUNCTION guard_callback_destination_current();

CREATE FUNCTION sync_callback_destination_version() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE frozen callback_destination_versions%ROWTYPE;
BEGIN
 INSERT INTO callback_destination_versions(tenant,app,destination_id,policy_version,key_version,canonical_url,key_ref,revoked_at)
 VALUES(NEW.tenant,NEW.app,NEW.id,NEW.policy_version,NEW.key_version,NEW.canonical_url,NEW.key_ref,CASE WHEN NEW.state='revoked' THEN clock_timestamp() END)
 ON CONFLICT(tenant,app,destination_id,policy_version,key_version) DO NOTHING;
 SELECT * INTO STRICT frozen FROM callback_destination_versions
 WHERE tenant=NEW.tenant AND app=NEW.app AND destination_id=NEW.id
  AND policy_version=NEW.policy_version AND key_version=NEW.key_version FOR UPDATE;
 IF frozen.canonical_url IS DISTINCT FROM NEW.canonical_url OR frozen.key_ref IS DISTINCT FROM NEW.key_ref THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMMUTABLE_DESTINATION_VERSION';
 END IF;
 IF frozen.revoked_at IS NOT NULL AND NEW.state<>'revoked' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='DESTINATION_VERSION_REVOKED';
 END IF;
 IF NEW.state='revoked' AND frozen.revoked_at IS NULL THEN
  UPDATE callback_destination_versions SET revoked_at=clock_timestamp()
  WHERE tenant=NEW.tenant AND app=NEW.app AND destination_id=NEW.id
   AND policy_version=NEW.policy_version AND key_version=NEW.key_version;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER destination_version_sync AFTER INSERT OR UPDATE ON callback_destinations
 FOR EACH ROW EXECUTE FUNCTION sync_callback_destination_version();

-- Pin invoker-rights trigger resolution to the migration's trusted schema.
-- pg_temp is explicitly LAST so a store connection cannot shadow these tables.
DO $$ DECLARE fn text; BEGIN
 FOREACH fn IN ARRAY ARRAY['guard_callback_destination_version','guard_callback_destination_current','sync_callback_destination_version'] LOOP
  EXECUTE format('ALTER FUNCTION %I.%I() SET search_path TO pg_catalog, %I, pg_temp',current_schema(),fn,current_schema());
 END LOOP;
 EXECUTE format('REVOKE ALL ON %I.callback_destination_versions FROM PUBLIC',current_schema());
END $$;
