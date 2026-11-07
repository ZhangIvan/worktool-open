-- Approved narrow ADR: retain canonical TEXT bytes, validate object syntax via
-- json (not jsonb); escaped U+0000 must remain escaped, never a raw TEXT NUL.
-- Append-only migration. Caller must wrap each file in one transaction.
DO $migration$
DECLARE
 target_schema text := pg_catalog.current_schema();
 target_oid oid;
 payload_attnum smallint;
 found_count integer;
 old_def text;
 old_expr text;
 expected_expr constant text := '((payload IS NULL) OR (jsonb_typeof((payload)::jsonb) = ''object''::text))';
BEGIN
 PERFORM pg_catalog.set_config('lock_timeout','1000ms',true);
 PERFORM pg_catalog.set_config('statement_timeout','5000ms',true);
 IF target_schema IS NULL OR target_schema IN ('public','pg_catalog','information_schema') THEN
  RAISE EXCEPTION USING ERRCODE='P0001',MESSAGE='PAYLOAD_CHECK_GUARD_SCHEMA';
 END IF;
 target_oid := pg_catalog.to_regclass(pg_catalog.format('%I.commands',target_schema));
 IF target_oid IS NULL OR NOT EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE oid=target_oid AND relkind='r') THEN
  RAISE EXCEPTION USING ERRCODE='P0001',MESSAGE='PAYLOAD_CHECK_GUARD_TABLE';
 END IF;
 EXECUTE pg_catalog.format('LOCK TABLE %I.commands IN ACCESS EXCLUSIVE MODE',target_schema);
 SELECT attnum INTO payload_attnum FROM pg_catalog.pg_attribute
  WHERE attrelid=target_oid AND attname='payload' AND NOT attisdropped
    AND atttypid='pg_catalog.text'::pg_catalog.regtype AND atttypmod=-1;
 IF payload_attnum IS NULL THEN
  RAISE EXCEPTION USING ERRCODE='P0001',MESSAGE='PAYLOAD_CHECK_GUARD_COLUMN';
 END IF;
 SELECT count(*) INTO found_count FROM pg_catalog.pg_constraint
  WHERE conrelid=target_oid AND contype='c' AND conkey=ARRAY[payload_attnum]::smallint[];
 IF found_count<>1 THEN
  RAISE EXCEPTION USING ERRCODE='P0001',MESSAGE='PAYLOAD_CHECK_GUARD_COUNT';
 END IF;
 SELECT pg_catalog.pg_get_expr(conbin,conrelid),pg_catalog.pg_get_constraintdef(oid)
 INTO old_expr,old_def FROM pg_catalog.pg_constraint
 WHERE conrelid=target_oid AND conname='commands_payload_check' AND contype='c'
  AND convalidated AND conislocal AND coninhcount=0 AND NOT connoinherit
  AND conkey=ARRAY[payload_attnum]::smallint[];
 IF old_expr IS DISTINCT FROM expected_expr OR old_def IS DISTINCT FROM ('CHECK ('||expected_expr||')')
 OR EXISTS(SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid=target_oid AND conname='commands_payload_json_object') THEN
  RAISE EXCEPTION USING ERRCODE='P0001',MESSAGE='PAYLOAD_CHECK_GUARD_DEFINITION';
 END IF;
 EXECUTE pg_catalog.format('ALTER TABLE %I.commands DROP CONSTRAINT commands_payload_check',target_schema);
 EXECUTE pg_catalog.format('ALTER TABLE %I.commands ADD CONSTRAINT commands_payload_json_object CHECK(payload IS NULL OR pg_catalog.json_typeof(payload::pg_catalog.json)=''object'')',target_schema);
END
$migration$;
