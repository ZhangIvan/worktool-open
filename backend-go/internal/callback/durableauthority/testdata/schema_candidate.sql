-- UNEXECUTED ISOLATED CANDIDATE, not a migration or permission boundary.
-- No GRANT/role/credentials/SECURITY DEFINER. Never installed by this package.
-- Provisioning, actual writer exclusion and restore provenance are unresolved.
CREATE SCHEMA wt_synthetic_callback_authority;
CREATE TABLE wt_synthetic_callback_authority.registry (
 scope_id text PRIMARY KEY CHECK (scope_id ~ '^[A-Za-z0-9_-]{1,128}$'),
 allocation_id text NOT NULL CHECK (allocation_id ~ '^[A-Za-z0-9_-]{1,128}$'),
 instance_id text NOT NULL CHECK (instance_id ~ '^[A-Za-z0-9_-]{1,128}$'),
 boot_id text NOT NULL CHECK (boot_id ~ '^[A-Za-z0-9_-]{1,128}$'),
 epoch_id text NOT NULL CHECK (epoch_id ~ '^[A-Za-z0-9_-]{1,128}$'),
 provenance_ref text NOT NULL CHECK (provenance_ref ~ '^[A-Za-z0-9_-]{1,128}$'),
 registry_revision bigint NOT NULL CHECK (registry_revision > 0),
 plan_identity text NOT NULL CHECK (plan_identity ~ '^cbplan-v1:sha256:[0-9a-f]{64}$'),
 binding_identity text NOT NULL CHECK (binding_identity ~ '^[0-9a-f]{64}$'),
 plan_raw bytea NOT NULL CHECK (octet_length(plan_raw) BETWEEN 1 AND 262144),
 descriptors_raw bytea NOT NULL CHECK (octet_length(descriptors_raw) BETWEEN 1 AND 524288),
 UNIQUE (scope_id,allocation_id,instance_id,boot_id,epoch_id,registry_revision,binding_identity)
);
CREATE TABLE wt_synthetic_callback_authority.ledger (
 scope_id text PRIMARY KEY,
 allocation_id text NOT NULL, instance_id text NOT NULL, boot_id text NOT NULL,
 epoch_id text NOT NULL, registry_revision bigint NOT NULL, binding_identity text NOT NULL,
 state text NOT NULL CHECK (state IN ('running','halted')),
 halt_code text NOT NULL,
 CHECK ((state='running' AND halt_code='') OR (state='halted' AND halt_code IN ('JOINED','UNCERTAINTY','CLEANUP_UNCONFIRMED','START_INTERRUPTED'))),
 FOREIGN KEY (scope_id,allocation_id,instance_id,boot_id,epoch_id,registry_revision,binding_identity)
 REFERENCES wt_synthetic_callback_authority.registry(scope_id,allocation_id,instance_id,boot_id,epoch_id,registry_revision,binding_identity),
 UNIQUE (scope_id,allocation_id,instance_id,boot_id,epoch_id,registry_revision,binding_identity)
);
CREATE TABLE wt_synthetic_callback_authority.events (
 -- One fixed authority/schema, no caller-selected namespace: global op uniqueness.
 operation_id text PRIMARY KEY CHECK (operation_id ~ '^[A-Za-z0-9_-]{1,128}$'),
 scope_id text NOT NULL, allocation_id text NOT NULL, instance_id text NOT NULL,
 boot_id text NOT NULL, epoch_id text NOT NULL, registry_revision bigint NOT NULL,
 binding_identity text NOT NULL,
 kind text NOT NULL, code text NOT NULL,
 evidence_ref text NOT NULL CHECK (evidence_ref ~ '^[A-Za-z0-9_-]{1,128}$'),
 CHECK ((kind='incident' AND code IN ('UNCERTAINTY','CLEANUP_UNCONFIRMED','START_INTERRUPTED')) OR (kind='joined' AND code='JOINED') OR (kind='reconciliation_fact' AND code='EVIDENCE_RECORDED')),
 FOREIGN KEY (scope_id,allocation_id,instance_id,boot_id,epoch_id,registry_revision,binding_identity)
 REFERENCES wt_synthetic_callback_authority.ledger(scope_id,allocation_id,instance_id,boot_id,epoch_id,registry_revision,binding_identity)
);
CREATE INDEX callback_authority_event_scope ON wt_synthetic_callback_authority.events(scope_id);
CREATE INDEX callback_authority_incident_scope ON wt_synthetic_callback_authority.events(scope_id) WHERE kind='incident';

CREATE INDEX callback_authority_terminal_scope ON wt_synthetic_callback_authority.events(scope_id,code) WHERE kind IN ('incident','joined');

CREATE FUNCTION wt_synthetic_callback_authority.immutable_record() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog,pg_temp AS $$
 BEGIN RAISE EXCEPTION 'callback authority immutable history' USING ERRCODE='23514'; END $$;
CREATE TRIGGER registry_immutable BEFORE UPDATE OR DELETE ON wt_synthetic_callback_authority.registry FOR EACH ROW EXECUTE FUNCTION wt_synthetic_callback_authority.immutable_record();
CREATE TRIGGER registry_no_truncate BEFORE TRUNCATE ON wt_synthetic_callback_authority.registry FOR EACH STATEMENT EXECUTE FUNCTION wt_synthetic_callback_authority.immutable_record();
CREATE TRIGGER events_immutable BEFORE UPDATE OR DELETE ON wt_synthetic_callback_authority.events FOR EACH ROW EXECUTE FUNCTION wt_synthetic_callback_authority.immutable_record();
CREATE TRIGGER events_no_truncate BEFORE TRUNCATE ON wt_synthetic_callback_authority.events FOR EACH STATEMENT EXECUTE FUNCTION wt_synthetic_callback_authority.immutable_record();
CREATE TRIGGER ledger_no_delete BEFORE DELETE ON wt_synthetic_callback_authority.ledger FOR EACH ROW EXECUTE FUNCTION wt_synthetic_callback_authority.immutable_record();
CREATE TRIGGER ledger_no_truncate BEFORE TRUNCATE ON wt_synthetic_callback_authority.ledger FOR EACH STATEMENT EXECUTE FUNCTION wt_synthetic_callback_authority.immutable_record();
CREATE FUNCTION wt_synthetic_callback_authority.ledger_monotone() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog,pg_temp AS $$
 BEGIN
  IF TG_OP='INSERT' THEN
   IF NEW.state<>'running' OR NEW.halt_code<>'' THEN RAISE EXCEPTION 'callback authority invalid initial intent' USING ERRCODE='23514'; END IF;
  ELSE
   IF ROW(NEW.scope_id,NEW.allocation_id,NEW.instance_id,NEW.boot_id,NEW.epoch_id,NEW.registry_revision,NEW.binding_identity)
     IS DISTINCT FROM ROW(OLD.scope_id,OLD.allocation_id,OLD.instance_id,OLD.boot_id,OLD.epoch_id,OLD.registry_revision,OLD.binding_identity)
     OR OLD.state<>'running' OR OLD.halt_code<>'' OR NEW.state<>'halted' OR NEW.halt_code='' THEN
    RAISE EXCEPTION 'callback authority nonmonotone update' USING ERRCODE='23514';
   END IF;
  END IF;
  RETURN NEW;
 END $$;
CREATE TRIGGER ledger_monotone BEFORE INSERT OR UPDATE ON wt_synthetic_callback_authority.ledger FOR EACH ROW EXECUTE FUNCTION wt_synthetic_callback_authority.ledger_monotone();
-- These guards do not authenticate input, canonical hashes or a privileged DB
-- owner. Actual nonowner/direct-DML, concurrency and SQL syntax remain NOT_RUN.
