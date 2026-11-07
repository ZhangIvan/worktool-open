-- P0 v1.1: empty-schema path only, no import of unreserved historical deliveries.
CREATE TABLE callback_subscription_versions (
 tenant text NOT NULL, app text NOT NULL, subscription_id text NOT NULL,
 version bigint NOT NULL CHECK(version>0), destination_id text NOT NULL,
 destination_policy_version bigint NOT NULL CHECK(destination_policy_version>0),
 key_version bigint NOT NULL CHECK(key_version>0),
 event_type text NOT NULL CHECK(event_type='command.result'),
 event_schema_version text NOT NULL CHECK(event_schema_version='bounded-result-v1'),
 max_events_per_child integer NOT NULL CHECK(max_events_per_child=1),
 max_body_bytes integer NOT NULL CHECK(max_body_bytes=65536),
 max_attempts integer NOT NULL CHECK(max_attempts BETWEEN 1 AND 4),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), retired_at timestamptz,
 PRIMARY KEY(tenant,app,subscription_id,version),
 FOREIGN KEY(tenant,app) REFERENCES developer_apps(tenant,id),
 FOREIGN KEY(tenant,app,destination_id) REFERENCES callback_destinations(tenant,app,id),
 UNIQUE(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,max_attempts)
);
CREATE UNIQUE INDEX subscription_active_version ON callback_subscription_versions(tenant,app,subscription_id) WHERE retired_at IS NULL;
CREATE UNIQUE INDEX subscription_active_destination ON callback_subscription_versions(tenant,app,destination_id,event_type) WHERE retired_at IS NULL;
CREATE TABLE admission_reservations (
 tenant text NOT NULL, reservation_id text NOT NULL, command text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('result','callback')),
 app text, subscription_id text, subscription_version bigint,
 destination_id text, destination_policy_version bigint, key_version bigint, max_attempts integer,
 event_ordinal integer NOT NULL CHECK(event_ordinal=0),
 state text NOT NULL CHECK(state IN ('held','materialized','cancelled_retained','purged')),
 reserved_jobs integer NOT NULL CHECK(reserved_jobs=1), reserved_bytes bigint NOT NULL,
 delivery_id text, event_id text,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), materialized_at timestamptz,
 retain_until timestamptz, released_at timestamptz, release_reason text,
 PRIMARY KEY(tenant,reservation_id),
 FOREIGN KEY(tenant,command) REFERENCES commands(tenant,id),
 FOREIGN KEY(tenant,app,command) REFERENCES commands(tenant,app_id,id),
 FOREIGN KEY(tenant,app,subscription_id,subscription_version,destination_id,destination_policy_version,key_version,max_attempts)
  REFERENCES callback_subscription_versions(tenant,app,subscription_id,version,destination_id,destination_policy_version,key_version,max_attempts),
 CHECK((kind='result' AND app IS NULL AND subscription_id IS NULL AND subscription_version IS NULL AND destination_id IS NULL AND destination_policy_version IS NULL AND key_version IS NULL AND max_attempts IS NULL AND reserved_bytes=196608 AND delivery_id IS NULL AND event_id IS NULL)
 OR (kind='callback' AND app IS NOT NULL AND subscription_id IS NOT NULL AND subscription_version>0 AND destination_id IS NOT NULL AND destination_policy_version>0 AND key_version>0 AND max_attempts BETWEEN 1 AND 4 AND reserved_bytes=90112
 AND subscription_version IS NOT NULL AND destination_policy_version IS NOT NULL AND key_version IS NOT NULL AND max_attempts IS NOT NULL)),
 CHECK((kind='callback' AND state='materialized' AND delivery_id IS NOT NULL AND event_id IS NOT NULL)
 OR ((kind='result' OR state<>'materialized') AND delivery_id IS NULL AND event_id IS NULL)),
 CHECK((state='purged')=(released_at IS NOT NULL)),
 CHECK(state<>'purged' OR release_reason IS NOT NULL),
 UNIQUE(tenant,reservation_id,kind,state,app,command,destination_id,event_ordinal,subscription_id,subscription_version,destination_policy_version,key_version,max_attempts)
);
CREATE UNIQUE INDEX reservation_one_result ON admission_reservations(tenant,command) WHERE kind='result';
CREATE UNIQUE INDEX reservation_one_callback ON admission_reservations(tenant,command,destination_id,event_ordinal) WHERE kind='callback';
CREATE UNIQUE INDEX reservation_one_delivery ON admission_reservations(tenant,delivery_id) WHERE delivery_id IS NOT NULL;
ALTER TABLE scope_limits ADD COLUMN capacity_jobs bigint NOT NULL DEFAULT 0 CHECK(capacity_jobs>=0),
 ADD COLUMN capacity_bytes bigint NOT NULL DEFAULT 0 CHECK(capacity_bytes>=0),
 ADD COLUMN charged_jobs bigint NOT NULL DEFAULT 0 CHECK(charged_jobs>=0),
 ADD COLUMN charged_bytes bigint NOT NULL DEFAULT 0 CHECK(charged_bytes>=0),
 ADD COLUMN admission_enabled boolean NOT NULL DEFAULT false,
 ADD COLUMN admission_policy_version bigint NOT NULL DEFAULT 1 CHECK(admission_policy_version>0),
 ADD CHECK(charged_jobs<=capacity_jobs AND charged_bytes<=capacity_bytes);
ALTER TABLE commands ADD COLUMN subscription_snapshot jsonb NOT NULL,
 ADD COLUMN subscription_snapshot_digest text NOT NULL,
 ADD COLUMN admission_version text NOT NULL,
 ADD CHECK(jsonb_typeof(subscription_snapshot)='array' AND jsonb_array_length(subscription_snapshot)<=4 AND octet_length(subscription_snapshot::text)<=16384),
 ADD CHECK(length(subscription_snapshot_digest)=64 AND subscription_snapshot_digest ~ '^[0-9a-f]{64}$'),
 ADD CHECK(admission_version='bounded-result-v1');
ALTER TABLE callback_outbox ADD COLUMN reservation_id text NOT NULL,
 ADD COLUMN reservation_kind text NOT NULL CHECK(reservation_kind='callback'),
 ADD COLUMN reservation_state text NOT NULL CHECK(reservation_state='materialized'),
 ADD COLUMN subscription_id text NOT NULL,
 ADD COLUMN subscription_version bigint NOT NULL CHECK(subscription_version>0),
 ADD COLUMN event_ordinal integer NOT NULL CHECK(event_ordinal=0);
