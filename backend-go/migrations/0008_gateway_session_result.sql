-- Candidate only: no production backfill of previously used session tokens.
SET LOCAL lock_timeout='1s';
SET LOCAL statement_timeout='5s';
LOCK TABLE device_sessions, commands IN ACCESS EXCLUSIVE MODE;
DO $$
DECLARE target oid; att smallint; expr text; n integer;
BEGIN
 IF current_schema() IN ('public','pg_catalog','information_schema') THEN RAISE EXCEPTION 'GATEWAY_SCHEMA_GUARD'; END IF;
 IF EXISTS(SELECT 1 FROM device_sessions WHERE NOT revoked) THEN RAISE EXCEPTION 'GATEWAY_EXISTING_SESSION_REQUIRES_REVOCATION'; END IF;
 SELECT c.oid INTO target FROM pg_class c JOIN pg_namespace ns ON ns.oid=c.relnamespace WHERE ns.nspname=current_schema() AND c.relname='commands' AND c.relkind='r';
 IF target IS NULL THEN RAISE EXCEPTION 'RESULT_CHECK_GUARD_TABLE'; END IF;
 SELECT attnum INTO att FROM pg_attribute WHERE attrelid=target AND attname='result' AND NOT attisdropped AND atttypid='text'::regtype AND atttypmod=-1;
 IF att IS NULL THEN RAISE EXCEPTION 'RESULT_CHECK_GUARD_TYPE'; END IF;
 SELECT count(*) INTO n FROM pg_constraint WHERE conrelid=target AND contype='c' AND conkey=ARRAY[att]::smallint[];
 IF n<>1 THEN RAISE EXCEPTION 'RESULT_CHECK_GUARD_COUNT'; END IF;
 SELECT pg_get_expr(conbin,conrelid) INTO expr FROM pg_constraint WHERE conrelid=target AND conname='commands_result_check' AND contype='c' AND conkey=ARRAY[att]::smallint[] AND convalidated AND conislocal AND coninhcount=0;
 IF expr IS DISTINCT FROM '((result IS NULL) OR (jsonb_typeof((result)::jsonb) = ''object''::text))' THEN RAISE EXCEPTION 'RESULT_CHECK_GUARD_DEFINITION'; END IF;
 IF EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=target AND conname='commands_result_json_object') THEN RAISE EXCEPTION 'RESULT_CHECK_GUARD_NEW_NAME'; END IF;
END $$;
ALTER TABLE commands DROP CONSTRAINT commands_result_check;
ALTER TABLE commands ADD CONSTRAINT commands_result_json_object CHECK(result IS NULL OR pg_catalog.json_typeof(result::pg_catalog.json)='object');
ALTER TABLE device_sessions ADD COLUMN ws_consumed boolean NOT NULL DEFAULT false;
ALTER TABLE device_sessions ADD COLUMN ws_connection_id text;
ALTER TABLE device_sessions ADD COLUMN ws_recv_seq bigint NOT NULL DEFAULT 0 CHECK(ws_recv_seq BETWEEN 0 AND 9007199254740991);
ALTER TABLE device_sessions ADD CONSTRAINT ws_consumption_complete CHECK(ws_consumed=(ws_connection_id IS NOT NULL));
CREATE FUNCTION guard_ws_consumption() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.ws_consumed AND (NOT NEW.ws_consumed OR NEW.ws_connection_id IS DISTINCT FROM OLD.ws_connection_id) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='WS_SESSION_CANNOT_REACTIVATE';
 END IF;
 IF NEW.ws_recv_seq<OLD.ws_recv_seq THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='WS_SEQUENCE_REGRESSION'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER ws_consumption_immutable BEFORE UPDATE ON device_sessions FOR EACH ROW EXECUTE FUNCTION guard_ws_consumption();
