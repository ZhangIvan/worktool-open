-- CANDIDATE: execute only in a fresh isolated schema under explicit approval.
CREATE TABLE runtime_instances (
  instance_id text PRIMARY KEY, -- opaque boot-unique process ID, NOT UUID type
  roles text[] NOT NULL CHECK(cardinality(roles)>0 AND roles <@ ARRAY['api','gateway','worker']::text[]),
  boot_id text NOT NULL UNIQUE,
  recovery_epoch text NOT NULL, -- injected new deployment recovery generation, never trusted merely because restored from backup
  started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  heartbeat_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  state text NOT NULL CHECK(state IN ('starting','active','draining','stopped'))
);
CREATE TABLE device_gateway_leases (
  tenant text NOT NULL,
  device text NOT NULL,
  owner_instance text NOT NULL REFERENCES runtime_instances(instance_id),
  session_id text NOT NULL,
  recovery_epoch text NOT NULL,
  fence bigint NOT NULL CHECK(fence>0), -- monotonically increases per tenant/device takeover; never reset by delete/recreate
  lease_until timestamptz NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  PRIMARY KEY(tenant,device),
  FOREIGN KEY(tenant,device) REFERENCES devices(tenant,id),
  FOREIGN KEY(tenant,device,session_id) REFERENCES device_sessions(tenant,device,id)
);
-- NEW fields on existing tables: snapshots are NOT independent ownership authorities.
ALTER TABLE device_sessions ADD COLUMN gateway_owner text REFERENCES runtime_instances(instance_id);
ALTER TABLE device_sessions ADD COLUMN gateway_fence bigint CHECK(gateway_fence>0);
ALTER TABLE device_sessions ADD COLUMN gateway_lease_until timestamptz;
ALTER TABLE device_sessions ADD CONSTRAINT gateway_snapshot_complete CHECK(
 (gateway_owner IS NULL AND gateway_fence IS NULL AND gateway_lease_until IS NULL) OR
 (gateway_owner IS NOT NULL AND gateway_fence IS NOT NULL AND gateway_lease_until IS NOT NULL));
ALTER TABLE commands ADD COLUMN command_version bigint NOT NULL DEFAULT 0 CHECK(command_version>=0);
ALTER TABLE commands ADD COLUMN dispatch_session text;
ALTER TABLE commands ADD COLUMN dispatch_fence bigint CHECK(dispatch_fence>0);
ALTER TABLE commands ADD COLUMN dispatch_owner text REFERENCES runtime_instances(instance_id);
ALTER TABLE commands ADD COLUMN dispatch_recovery_epoch text;
ALTER TABLE commands ADD CONSTRAINT dispatch_session_binding FOREIGN KEY(tenant,device,dispatch_session)
 REFERENCES device_sessions(tenant,device,id);
ALTER TABLE commands ADD CONSTRAINT dispatch_snapshot_complete CHECK(
 (dispatch_session IS NULL AND dispatch_fence IS NULL AND dispatch_owner IS NULL AND dispatch_recovery_epoch IS NULL) OR
 (dispatch_session IS NOT NULL AND dispatch_fence IS NOT NULL AND dispatch_owner IS NOT NULL AND dispatch_recovery_epoch IS NOT NULL));

CREATE TABLE callback_destinations (
  tenant text NOT NULL,
  id text NOT NULL,
  app text NOT NULL,
  canonical_url text NOT NULL, -- sensitive config; never from event payload, never auto-import as approved
  policy_version bigint NOT NULL CHECK(policy_version>0),
  key_ref text NOT NULL, -- opaque production secret-provider handle, NOT raw HMAC key
  key_version bigint NOT NULL CHECK(key_version>0),
  state text NOT NULL CHECK(state IN ('pending_review','active','revoked')),
  enabled boolean GENERATED ALWAYS AS (state='active') STORED,
  PRIMARY KEY(tenant,id),
  UNIQUE(tenant,app,id),
  FOREIGN KEY(tenant,app) REFERENCES developer_apps(tenant,id)
);
CREATE TABLE callback_outbox (
  tenant text NOT NULL,
  delivery_id text NOT NULL,
  event_id text NOT NULL,
  destination_id text NOT NULL,
  app text NOT NULL,
  command text NOT NULL,
  device text NOT NULL,
  app_epoch bigint NOT NULL CHECK(app_epoch>=0),
  device_epoch bigint NOT NULL CHECK(device_epoch>=0),
  body_bytes bytea NOT NULL CHECK(octet_length(body_bytes)<=65536), -- EXACT immutable bytes signed and sent, never jsonb reserialization
  payload_digest text NOT NULL CHECK(length(payload_digest)=64 AND payload_digest ~ '^[0-9a-f]{64}$'),
  policy_version bigint NOT NULL CHECK(policy_version>0),
  key_version bigint NOT NULL CHECK(key_version>0),
  state text NOT NULL DEFAULT 'ready' CHECK(state IN ('ready','leased','delivered','dead_letter','cancelled')),
  available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  max_attempts integer NOT NULL DEFAULT 4 CHECK(max_attempts BETWEEN 1 AND 4),
  attempt_count integer NOT NULL DEFAULT 0 CHECK(attempt_count>=0 AND attempt_count<=max_attempts),
  expires_at timestamptz NOT NULL,
  claimed_at timestamptz,
  hard_deadline_at timestamptz,
  lease_owner text REFERENCES runtime_instances(instance_id),
  lease_recovery_epoch text,
  lease_until timestamptz,
  fence bigint NOT NULL DEFAULT 0 CHECK(fence>=0),
  delivered_at timestamptz,
  last_error_code text,
  PRIMARY KEY(tenant,delivery_id),
  UNIQUE(tenant,event_id,destination_id),
  FOREIGN KEY(tenant,event_id) REFERENCES developer_callbacks(tenant,event),
  FOREIGN KEY(tenant,app,command) REFERENCES commands(tenant,app_id,id),
  FOREIGN KEY(tenant,device,command) REFERENCES commands(tenant,device,id),
  FOREIGN KEY(tenant,app,destination_id) REFERENCES callback_destinations(tenant,app,id),
  CHECK((lease_owner IS NULL)=(lease_until IS NULL)),
  CHECK((lease_owner IS NULL)=(lease_recovery_epoch IS NULL)),
  CHECK(state<>'leased' OR (lease_owner IS NOT NULL AND lease_until IS NOT NULL AND fence>0 AND claimed_at IS NOT NULL AND hard_deadline_at IS NOT NULL)),
  CHECK(lease_until IS NULL OR (hard_deadline_at IS NOT NULL AND lease_until<=hard_deadline_at AND hard_deadline_at<=expires_at)),
  CHECK(claimed_at IS NULL OR hard_deadline_at>=claimed_at)
);
CREATE TABLE callback_attempts (
  tenant text NOT NULL,
  delivery_id text NOT NULL,
  fence bigint NOT NULL CHECK(fence>0),
  attempt_no integer NOT NULL CHECK(attempt_no BETWEEN 1 AND 4),
  recovery_epoch text NOT NULL,
  owner_instance text NOT NULL REFERENCES runtime_instances(instance_id),
  started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  send_prepared_at timestamptz, -- committed authorization recheck marker, NOT proof HTTP was sent
  finished_at timestamptz,
  outcome text NOT NULL CHECK(outcome IN ('in_progress','delivered','retryable_failure','permanent_failure','lease_lost','delivery_unknown')),
  status_code integer CHECK(status_code BETWEEN 100 AND 599),
  error_code text,
  PRIMARY KEY(tenant,delivery_id,fence),
  UNIQUE(tenant,delivery_id,attempt_no),
  FOREIGN KEY(tenant,delivery_id) REFERENCES callback_outbox(tenant,delivery_id)
);
-- Shared admission/fairness state, NOT per-process counters. Application must lock in fixed order.
CREATE TABLE scope_limits (
  scope_kind text NOT NULL CHECK(scope_kind IN ('tenant','destination')),
  tenant text NOT NULL REFERENCES tenants(id),
  scope_id text NOT NULL,
  destination_id text,
  max_in_flight integer NOT NULL CHECK(max_in_flight>0),
  weight integer NOT NULL DEFAULT 1 CHECK(weight BETWEEN 1 AND 1000),
  enabled boolean NOT NULL DEFAULT false,
  rate_per_sec numeric(20,6) NOT NULL CHECK(rate_per_sec>0),
  burst numeric(20,6) NOT NULL CHECK(burst>=1),
  tokens numeric(20,6) NOT NULL CHECK(tokens>=0 AND tokens<=burst),
  refill_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  blocked_until timestamptz,
  virtual_finish numeric(30,6) NOT NULL DEFAULT 0 CHECK(virtual_finish>=0),
  policy_version bigint NOT NULL CHECK(policy_version>0),
  PRIMARY KEY(scope_kind,tenant,scope_id),
  FOREIGN KEY(tenant,destination_id) REFERENCES callback_destinations(tenant,id),
  CHECK((scope_kind='tenant' AND scope_id=tenant AND destination_id IS NULL) OR
        (scope_kind='destination' AND destination_id IS NOT NULL AND scope_id=destination_id))
);
CREATE TABLE scope_slots (
  scope_kind text NOT NULL,
  tenant text NOT NULL,
  scope_id text NOT NULL,
  slot_no integer NOT NULL CHECK(slot_no>=0),
  owner_instance text REFERENCES runtime_instances(instance_id),
  delivery_id text,
  delivery_fence bigint CHECK(delivery_fence>0),
  recovery_epoch text,
  slot_fence bigint NOT NULL DEFAULT 0 CHECK(slot_fence>=0),
  lease_until timestamptz,
  PRIMARY KEY(scope_kind,tenant,scope_id,slot_no),
  FOREIGN KEY(scope_kind,tenant,scope_id) REFERENCES scope_limits(scope_kind,tenant,scope_id),
  FOREIGN KEY(tenant,delivery_id) REFERENCES callback_outbox(tenant,delivery_id),
  CHECK((owner_instance IS NULL AND delivery_id IS NULL AND delivery_fence IS NULL AND lease_until IS NULL AND recovery_epoch IS NULL) OR
        (owner_instance IS NOT NULL AND delivery_id IS NOT NULL AND delivery_fence IS NOT NULL AND lease_until IS NOT NULL AND recovery_epoch IS NOT NULL))
);
-- Slot count/slot_no<max_in_flight is a cross-row invariant enforced by a locked policy update transaction,
-- NOT a PostgreSQL CHECK referencing another table. Expired rows are fenced, never simply reused unchecked.

ALTER TABLE developer_callbacks ADD CONSTRAINT legacy_event_app_command_identity UNIQUE(tenant,event,app,command);
ALTER TABLE callback_outbox ADD CONSTRAINT delivery_event_same_command FOREIGN KEY(tenant,event_id,app,command) REFERENCES developer_callbacks(tenant,event,app,command);

-- New immutable-body guard, scoped only to the new production delivery snapshot.
CREATE FUNCTION prevent_callback_snapshot_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.tenant,NEW.delivery_id,NEW.event_id,NEW.destination_id,NEW.app,NEW.command,NEW.device,NEW.app_epoch,NEW.device_epoch,NEW.body_bytes,
        NEW.payload_digest,NEW.policy_version,NEW.key_version,NEW.max_attempts,NEW.expires_at)
    IS DISTINCT FROM ROW(OLD.tenant,OLD.delivery_id,OLD.event_id,OLD.destination_id,OLD.app,OLD.command,OLD.device,OLD.app_epoch,OLD.device_epoch,OLD.body_bytes,
        OLD.payload_digest,OLD.policy_version,OLD.key_version,OLD.max_attempts,OLD.expires_at) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMMUTABLE_CALLBACK_SNAPSHOT';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER callback_snapshot_immutable BEFORE UPDATE ON callback_outbox
 FOR EACH ROW EXECUTE FUNCTION prevent_callback_snapshot_rewrite();
