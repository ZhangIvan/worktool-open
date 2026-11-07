-- Candidate lease lane. No invented connection identities for historical rows.
LOCK TABLE device_gateway_leases, device_sessions, commands IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM device_gateway_leases) OR
    EXISTS(SELECT 1 FROM device_sessions WHERE gateway_owner IS NOT NULL) OR
    EXISTS(SELECT 1 FROM commands WHERE dispatch_session IS NOT NULL) THEN
  RAISE EXCEPTION USING ERRCODE='P0001', MESSAGE='LEASE_CONNECTION_MIGRATION_REQUIRES_RECONCILIATION';
 END IF;
END $$;
ALTER TABLE device_gateway_leases ADD COLUMN connection_id text NOT NULL CHECK(length(connection_id) BETWEEN 1 AND 128);
ALTER TABLE device_gateway_leases ADD COLUMN state text NOT NULL CHECK(state IN ('active','retired'));
ALTER TABLE device_sessions ADD COLUMN gateway_connection text;
ALTER TABLE device_sessions DROP CONSTRAINT gateway_snapshot_complete;
ALTER TABLE device_sessions ADD CONSTRAINT gateway_snapshot_complete CHECK(
 (gateway_owner IS NULL AND gateway_fence IS NULL AND gateway_lease_until IS NULL AND gateway_connection IS NULL) OR
 (gateway_owner IS NOT NULL AND gateway_fence IS NOT NULL AND gateway_lease_until IS NOT NULL AND gateway_connection IS NOT NULL));
ALTER TABLE commands ADD COLUMN dispatch_connection text;
ALTER TABLE commands DROP CONSTRAINT dispatch_snapshot_complete;
ALTER TABLE commands ADD CONSTRAINT dispatch_snapshot_complete CHECK(
 (dispatch_session IS NULL AND dispatch_fence IS NULL AND dispatch_owner IS NULL AND dispatch_recovery_epoch IS NULL AND dispatch_connection IS NULL) OR
 (dispatch_session IS NOT NULL AND dispatch_fence IS NOT NULL AND dispatch_owner IS NOT NULL AND dispatch_recovery_epoch IS NOT NULL AND dispatch_connection IS NOT NULL));
CREATE UNIQUE INDEX device_one_nonrevoked_session ON device_sessions(tenant,device) WHERE NOT revoked;
CREATE UNIQUE INDEX device_one_unresolved_command ON commands(tenant,device) WHERE status IN ('delivered','accepted','running','execution_unknown');
CREATE FUNCTION guard_gateway_lease_identity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='LEASE_IDENTITY_IMMUTABLE'; END IF;
 IF ROW(NEW.tenant,NEW.device) IS DISTINCT FROM ROW(OLD.tenant,OLD.device) OR NEW.fence<OLD.fence THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='LEASE_FENCE_REGRESSION';
 END IF;
 IF NEW.fence=OLD.fence AND (OLD.state<>'active' OR NEW.state<>'active' OR
    ROW(NEW.owner_instance,NEW.session_id,NEW.connection_id,NEW.recovery_epoch) IS DISTINCT FROM
    ROW(OLD.owner_instance,OLD.session_id,OLD.connection_id,OLD.recovery_epoch)) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='LEASE_FENCE_REQUIRED';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER gateway_lease_identity BEFORE UPDATE OR DELETE ON device_gateway_leases FOR EACH ROW EXECUTE FUNCTION guard_gateway_lease_identity();
