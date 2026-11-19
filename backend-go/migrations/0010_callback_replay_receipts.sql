-- Independent, unexecuted candidate. Apply atomically before result history exists.
-- No backfill, deletion, release, role creation, or grant of additional authority.
SET LOCAL lock_timeout='1s';
SET LOCAL statement_timeout='5s';
DO $$
DECLARE ns text := current_schema(); relation_name text; expected oid;
BEGIN
 IF ns IS NULL OR ns IN ('public','pg_catalog','information_schema') OR ns LIKE 'pg_%'
 OR NOT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace n JOIN pg_catalog.pg_roles r ON r.oid=n.nspowner
               WHERE n.nspname=ns AND r.rolname=current_user)
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_namespace n,
           LATERAL pg_catalog.aclexplode(COALESCE(n.nspacl,pg_catalog.acldefault('n',n.nspowner))) a
           WHERE n.nspname=ns AND a.grantee=0 AND a.privilege_type='CREATE') THEN
  RAISE EXCEPTION 'REPLAY_RECEIPT_SCHEMA_GUARD';
 END IF;
 -- Refuse search-path/temp shadowing before any unqualified relation is locked.
 FOREACH relation_name IN ARRAY ARRAY['commands','admission_reservations','outbox','tombstones','command_events','inbox','developer_callbacks','callback_outbox','audit'] LOOP
  SELECT c.oid INTO expected FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname=ns AND c.relname=relation_name AND c.relkind='r';
  IF expected IS NULL OR pg_catalog.to_regclass(relation_name)::oid IS DISTINCT FROM expected THEN
   RAISE EXCEPTION 'REPLAY_RECEIPT_RELATION_GUARD';
  END IF;
 END LOOP;
END $$;
LOCK TABLE commands, admission_reservations, outbox, tombstones, command_events, inbox,
 developer_callbacks, callback_outbox, audit IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 -- A cleared commands.result is not evidence of an unused first-result slot.
 -- Conservative whole-schema checks intentionally require separate reconciliation.
 IF EXISTS(SELECT 1 FROM commands WHERE result IS NOT NULL
           OR status IN ('succeeded','partially_succeeded','failed','cancelled','expired','rejected','execution_unknown'))
 OR EXISTS(SELECT 1 FROM admission_reservations WHERE kind IN ('result','callback')
           AND state IN ('materialized','purged','cancelled_retained'))
 OR EXISTS(SELECT 1 FROM developer_callbacks)
 OR EXISTS(SELECT 1 FROM callback_outbox)
 OR EXISTS(SELECT 1 FROM audit WHERE action IN ('command.result','command.result_conflict'))
 OR EXISTS(SELECT 1 FROM outbox WHERE kind='result_ack')
 OR EXISTS(SELECT 1 FROM tombstones)
 OR EXISTS(SELECT 1 FROM command_events WHERE event IN
   ('result_success','result_partial','result_failure_no_effect','result_cancel_no_effect','lost_execution_evidence',
    'device_reject_before_start','device_proves_no_effect_cancel','device_proves_unstarted_expiry',
    'reconcile_applied','reconcile_partial','reconcile_not_applied',
    'reconcile_unstarted_rejected','reconcile_unstarted_cancelled','reconcile_unstarted_expired')
   OR source IN ('succeeded','partially_succeeded','failed','cancelled','expired','rejected','execution_unknown')
   OR target IN ('succeeded','partially_succeeded','failed','cancelled','expired','rejected','execution_unknown'))
 OR EXISTS(SELECT 1 FROM inbox WHERE (reply::pg_catalog.json->>'kind')='result_ack') THEN
  RAISE EXCEPTION 'REPLAY_RECEIPT_HISTORY_REQUIRES_RECONCILIATION';
 END IF;
END $$;

CREATE TABLE callback_replay_receipts (
 tenant text NOT NULL,
 command text NOT NULL,
 receipt text NOT NULL CHECK(octet_length(receipt) BETWEEN 2 AND 16384
                            AND pg_catalog.json_typeof(receipt::pg_catalog.json)='object'),
 PRIMARY KEY(tenant,command),
 FOREIGN KEY(tenant,command) REFERENCES commands(tenant,id)
);

CREATE FUNCTION guard_callback_replay_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c commands%ROWTYPE; body pg_catalog.json; field_name text;
BEGIN
 IF TG_OP<>'INSERT' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='REPLAY_RECEIPT_IMMUTABLE';
 END IF;
 IF NEW.receipt IS NULL OR octet_length(NEW.receipt)>16384 THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='REPLAY_RECEIPT_BOUNDS';
 END IF;
 body := NEW.receipt::pg_catalog.json;
 IF pg_catalog.json_typeof(body) IS DISTINCT FROM 'object' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='REPLAY_RECEIPT_OBJECT';
 END IF;
 IF EXISTS(SELECT 1 FROM pg_catalog.json_each(body) GROUP BY key HAVING count(*)<>1) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='REPLAY_RECEIPT_DUPLICATE_FIELD';
 END IF;
 FOREACH field_name IN ARRAY ARRAY['schema','tenant','device','command','app','attempt','commandDigest','resultDigest','status','effect'] LOOP
  IF pg_catalog.json_typeof(body->field_name) IS DISTINCT FROM 'string' THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='REPLAY_RECEIPT_FIELD_TYPE';
  END IF;
 END LOOP;
 IF pg_catalog.json_typeof(body->'appEpoch') IS DISTINCT FROM 'number' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='REPLAY_RECEIPT_FIELD_TYPE';
 END IF;
 SELECT * INTO c FROM commands WHERE tenant=NEW.tenant AND id=NEW.command FOR UPDATE;
 IF NOT FOUND OR c.actor_type IS DISTINCT FROM 'app' OR c.capability IS DISTINCT FROM 'message.send_text'
 OR c.admission_version IS DISTINCT FROM 'bounded-result-v1' OR c.result IS NULL OR c.app_id IS NULL
 OR c.app_epoch IS NULL OR c.attempt IS NULL
 OR (body->>'schema') IS DISTINCT FROM 'callback-result-replay-v1'
 OR (body->>'tenant') IS DISTINCT FROM c.tenant OR (body->>'device') IS DISTINCT FROM c.device
 OR (body->>'command') IS DISTINCT FROM c.id OR (body->>'app') IS DISTINCT FROM c.app_id
 OR (body->>'appEpoch') IS DISTINCT FROM c.app_epoch::text OR (body->>'attempt') IS DISTINCT FROM c.attempt
 OR (body->>'commandDigest') IS DISTINCT FROM c.digest
 OR (body->>'resultDigest') IS DISTINCT FROM pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(c.result,'UTF8')),'hex')
 OR (body->>'status') IS DISTINCT FROM c.status OR (body->>'effect') IS DISTINCT FROM c.effect THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='REPLAY_RECEIPT_COMMAND_BINDING';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER callback_replay_receipt_guard BEFORE INSERT OR UPDATE OR DELETE ON callback_replay_receipts
 FOR EACH ROW EXECUTE FUNCTION guard_callback_replay_receipt();

CREATE FUNCTION guard_replay_result_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.actor_type='app' AND OLD.capability='message.send_text' AND OLD.result IS NOT NULL THEN
  IF TG_OP='DELETE' THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='RESULT_IDENTITY_RETAINED';
  END IF;
  IF ROW(NEW.tenant,NEW.id,NEW.device,NEW.actor,NEW.actor_type,NEW.capability,NEW.app_id,NEW.app_epoch,
         NEW.attempt,NEW.digest,NEW.policy,NEW.confirm,NEW.created,NEW.expires,NEW.status,NEW.effect,NEW.result,
         NEW.dispatch_session,NEW.dispatch_owner,NEW.dispatch_fence,NEW.dispatch_recovery_epoch,NEW.dispatch_connection)
  IS DISTINCT FROM
     ROW(OLD.tenant,OLD.id,OLD.device,OLD.actor,OLD.actor_type,OLD.capability,OLD.app_id,OLD.app_epoch,
         OLD.attempt,OLD.digest,OLD.policy,OLD.confirm,OLD.created,OLD.expires,OLD.status,OLD.effect,OLD.result,
         OLD.dispatch_session,OLD.dispatch_owner,OLD.dispatch_fence,OLD.dispatch_recovery_epoch,OLD.dispatch_connection) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMMUTABLE_RESULT_IDENTITY';
  END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER replay_result_identity_guard BEFORE UPDATE OR DELETE ON commands
 FOR EACH ROW EXECUTE FUNCTION guard_replay_result_identity();

-- Invoker-rights functions, with trusted table lookup and pg_temp explicitly last.
DO $$ DECLARE fn text; BEGIN
 FOREACH fn IN ARRAY ARRAY['guard_callback_replay_receipt','guard_replay_result_identity'] LOOP
  EXECUTE format('ALTER FUNCTION %I.%I() SET search_path TO pg_catalog, %I, pg_temp',current_schema(),fn,current_schema());
 END LOOP;
 EXECUTE format('REVOKE ALL ON %I.callback_replay_receipts FROM PUBLIC',current_schema());
END $$;
