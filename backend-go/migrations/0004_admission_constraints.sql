ALTER TABLE callback_outbox ADD CONSTRAINT delivery_one_reservation UNIQUE(tenant,reservation_id);
ALTER TABLE callback_outbox ADD UNIQUE(tenant,delivery_id,reservation_id,event_id);
ALTER TABLE callback_outbox ADD CONSTRAINT delivery_reserved_identity FOREIGN KEY
 (tenant,reservation_id,reservation_kind,reservation_state,app,command,destination_id,event_ordinal,subscription_id,subscription_version,policy_version,key_version,max_attempts)
 REFERENCES admission_reservations
 (tenant,reservation_id,kind,state,app,command,destination_id,event_ordinal,subscription_id,subscription_version,destination_policy_version,key_version,max_attempts)
 DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE admission_reservations ADD CONSTRAINT reservation_exact_delivery FOREIGN KEY
 (tenant,delivery_id,reservation_id,event_id)
 REFERENCES callback_outbox(tenant,delivery_id,reservation_id,event_id)
 DEFERRABLE INITIALLY DEFERRED;
CREATE FUNCTION guard_reservation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='RESERVATION_IDENTITY_RETAINED'; END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'held' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='RESERVATION_INITIAL_STATE'; END IF;
  RETURN NEW;
 END IF;
 IF ROW(NEW.tenant,NEW.reservation_id,NEW.command,NEW.kind,NEW.app,NEW.subscription_id,NEW.subscription_version,NEW.destination_id,NEW.destination_policy_version,NEW.key_version,NEW.max_attempts,NEW.event_ordinal,NEW.reserved_jobs,NEW.reserved_bytes,NEW.created_at)
 IS DISTINCT FROM ROW(OLD.tenant,OLD.reservation_id,OLD.command,OLD.kind,OLD.app,OLD.subscription_id,OLD.subscription_version,OLD.destination_id,OLD.destination_policy_version,OLD.key_version,OLD.max_attempts,OLD.event_ordinal,OLD.reserved_jobs,OLD.reserved_bytes,OLD.created_at)
 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMMUTABLE_RESERVATION'; END IF;
 IF NOT(NEW.state=OLD.state OR (OLD.state='held' AND NEW.state IN ('materialized','cancelled_retained')) OR (OLD.state IN ('materialized','cancelled_retained') AND NEW.state='purged'))
 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='RESERVATION_STATE_TRANSITION'; END IF;
 IF OLD.state='purged' AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='PURGED_RESERVATION_IMMUTABLE'; END IF;
 IF OLD.state='materialized' AND NEW.state<>'purged' AND ROW(NEW.delivery_id,NEW.event_id) IS DISTINCT FROM ROW(OLD.delivery_id,OLD.event_id)
 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMMUTABLE_DELIVERY_LINK'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER reservation_guard BEFORE INSERT OR UPDATE OR DELETE ON admission_reservations FOR EACH ROW EXECUTE FUNCTION guard_reservation();
CREATE FUNCTION guard_subscription() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='SUBSCRIPTION_HISTORY_RETAINED'; END IF;
 IF (to_jsonb(NEW)-'retired_at') IS DISTINCT FROM (to_jsonb(OLD)-'retired_at') OR (OLD.retired_at IS NOT NULL AND NEW.retired_at IS DISTINCT FROM OLD.retired_at)
 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMMUTABLE_SUBSCRIPTION'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER subscription_guard BEFORE UPDATE OR DELETE ON callback_subscription_versions FOR EACH ROW EXECUTE FUNCTION guard_subscription();
CREATE FUNCTION guard_command_admission() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.subscription_snapshot,NEW.subscription_snapshot_digest,NEW.admission_version) IS DISTINCT FROM ROW(OLD.subscription_snapshot,OLD.subscription_snapshot_digest,OLD.admission_version)
 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMMUTABLE_ADMISSION_SNAPSHOT'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER command_admission_guard BEFORE UPDATE ON commands FOR EACH ROW EXECUTE FUNCTION guard_command_admission();
CREATE FUNCTION guard_delivery_admission() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.reservation_id,NEW.reservation_kind,NEW.reservation_state,NEW.subscription_id,NEW.subscription_version,NEW.event_ordinal)
 IS DISTINCT FROM ROW(OLD.reservation_id,OLD.reservation_kind,OLD.reservation_state,OLD.subscription_id,OLD.subscription_version,OLD.event_ordinal)
 THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='IMMUTABLE_DELIVERY_RESERVATION'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER delivery_admission_guard BEFORE UPDATE ON callback_outbox FOR EACH ROW EXECUTE FUNCTION guard_delivery_admission();
