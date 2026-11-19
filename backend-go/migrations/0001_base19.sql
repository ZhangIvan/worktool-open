-- CANDIDATE: execute only in a fresh isolated schema under explicit approval.
CREATE DOMAIN epoch_ms AS bigint CHECK(VALUE>=0 AND VALUE<=9007199254740991);
-- BASELINE TABLE: tenants; retention: retain while any tenant-owned rows exist; explicit tenant decommission, no cascade
CREATE TABLE tenants (
  id text PRIMARY KEY
);

-- BASELINE TABLE: users; retention: retain/inactivate; FK provenance and approval references prohibit earlydelete
CREATE TABLE users (
  tenant text NOT NULL,
  id text NOT NULL,
  login text NOT NULL,
  role text NOT NULL CHECK(role IN ('owner','admin','operator','approver','auditor')),
  password text NOT NULL,
  active boolean NOT NULL DEFAULT true,
  PRIMARY KEY(tenant,id),
  UNIQUE(tenant,login),
  FOREIGN KEY(tenant) REFERENCES tenants(id)
);

-- BASELINE TABLE: devices; retention: retain identity tombstone/revoked state; no automatic reenrollment or secret reuse
CREATE TABLE devices (
  tenant text NOT NULL,
  id text NOT NULL UNIQUE,
  name text NOT NULL,
  state text NOT NULL DEFAULT 'active',
  key_id text NOT NULL,
  secret text NOT NULL UNIQUE,
  epoch bigint NOT NULL DEFAULT 0,
  policy bigint NOT NULL DEFAULT 1,
  capabilities jsonb NOT NULL,
  PRIMARY KEY(tenant,id),
  FOREIGN KEY(tenant) REFERENCES tenants(id),
  CHECK(jsonb_typeof(capabilities)='array')
);

-- BASELINE TABLE: browser_sessions; retention: invalidate at cutover/restart; purge expired/revoked after30day metadata-policy review, rawCSRF neverpersisted
CREATE TABLE browser_sessions (
  token text PRIMARY KEY,
  tenant text NOT NULL,
  actor text NOT NULL,
  csrf text NOT NULL,
  expires epoch_ms NOT NULL,
  last_seen epoch_ms NOT NULL,
  FOREIGN KEY(tenant,actor) REFERENCES users(tenant,id)
);

-- BASELINE TABLE: enrollments; retention: invalidate pending at cutover; consumed/expired boundedcleanup after7days proposed
CREATE TABLE enrollments (
  id text PRIMARY KEY,
  tenant text NOT NULL,
  token text NOT NULL UNIQUE,
  capabilities jsonb NOT NULL,
  expires epoch_ms NOT NULL,
  consumed boolean NOT NULL DEFAULT false,
  FOREIGN KEY(tenant) REFERENCES tenants(id),
  CHECK(jsonb_typeof(capabilities)='array')
);

-- BASELINE TABLE: device_sessions; retention: revoke on cutover/restore; retain referenced dispatch/replay history at least7days and longer while referenced
CREATE TABLE device_sessions (
  id text PRIMARY KEY,
  tenant text NOT NULL,
  device text NOT NULL,
  token text NOT NULL UNIQUE,
  epoch bigint NOT NULL,
  expires epoch_ms NOT NULL,
  revoked boolean NOT NULL DEFAULT false,
  FOREIGN KEY(tenant,device) REFERENCES devices(tenant,id),
  UNIQUE(tenant,device,id)
);

-- BASELINE TABLE: developer_apps; retention: revoke/retain whileprovenance/jobsreference; rotate credentials oncutover
CREATE TABLE developer_apps (
  tenant text NOT NULL,
  id text NOT NULL,
  creator text NOT NULL,
  name text NOT NULL,
  token_hash text NOT NULL UNIQUE,
  active boolean NOT NULL DEFAULT true,
  auth_epoch bigint NOT NULL DEFAULT 0,
  token_expires epoch_ms NOT NULL,
  scopes jsonb NOT NULL,
  callback_url text,
  PRIMARY KEY(tenant,id),
  FOREIGN KEY(tenant,creator) REFERENCES users(tenant,id),
  UNIQUE(tenant,id,creator),
  CHECK(jsonb_typeof(scopes)='array')
);

-- BASELINE TABLE: developer_bindings; retention: current grants; scopeupdates audited; deletionnotpermissionhistoryerasure
CREATE TABLE developer_bindings (
  tenant text NOT NULL,
  app text NOT NULL,
  device text NOT NULL,
  PRIMARY KEY(tenant,app,device),
  FOREIGN KEY(tenant,app) REFERENCES developer_apps(tenant,id),
  FOREIGN KEY(tenant,device) REFERENCES devices(tenant,id)
);

-- BASELINE TABLE: commands; retention: terminal payload proposed30days; preserve identity/digest/effect/tombstone at least7days and all referenced/unknown rows pendingreconciliation
CREATE TABLE commands (
  tenant text NOT NULL,
  id text NOT NULL,
  device text NOT NULL,
  actor text NOT NULL,
  capability text NOT NULL CHECK(capability IN ('message.send_text','file.export')),
  payload text,
  digest text NOT NULL,
  policy bigint NOT NULL,
  confirm boolean NOT NULL,
  status text NOT NULL,
  effect text NOT NULL DEFAULT 'not_started',
  attempt text,
  created epoch_ms NOT NULL,
  expires epoch_ms NOT NULL,
  result text,
  cancel_requested boolean NOT NULL DEFAULT false,
  PRIMARY KEY(tenant,id),
  FOREIGN KEY(tenant,device) REFERENCES devices(tenant,id),
  FOREIGN KEY(tenant,actor) REFERENCES users(tenant,id),
  actor_type text NOT NULL DEFAULT 'user' CHECK(actor_type IN ('user','app')),
  app_epoch bigint,
  app_id text,
  UNIQUE(tenant,device,id),
  FOREIGN KEY(tenant,app_id,actor) REFERENCES developer_apps(tenant,id,creator),
  CHECK((actor_type='user' AND app_id IS NULL AND app_epoch IS NULL) OR (actor_type='app' AND app_id IS NOT NULL AND app_epoch IS NOT NULL)),
  CHECK(payload IS NULL OR jsonb_typeof(payload::jsonb)='object'),
  CHECK(result IS NULL OR jsonb_typeof(result::jsonb)='object')
);

-- BASELINE TABLE: command_events; retention: at leastcommand lifetime+replaywindow; no cleanupofhistory requiredforunstartedreconcile
CREATE TABLE command_events (
  tenant text NOT NULL,
  command text NOT NULL,
  event_id text NOT NULL,
  source text NOT NULL,
  event text NOT NULL,
  target text NOT NULL,
  created epoch_ms NOT NULL,
  PRIMARY KEY(tenant,command,event_id),
  FOREIGN KEY(tenant,command) REFERENCES commands(tenant,id)
);

-- BASELINE TABLE: idempotency; retention: expires is existing intended24h horizon; cleanupneeds7day replaytombstone or epochguarantee; do not silently shorten current noGC behavior
CREATE TABLE idempotency (
  tenant text NOT NULL,
  actor text NOT NULL,
  operation text NOT NULL,
  key text NOT NULL,
  digest text NOT NULL,
  resource text NOT NULL,
  expires epoch_ms NOT NULL,
  PRIMARY KEY(tenant,actor,operation,key),
  FOREIGN KEY(tenant,actor) REFERENCES users(tenant,id)
);

-- BASELINE TABLE: tombstones; retention: retain_until >=7days; never deletewhile unknown/unsettled; minimumnotmaximum
CREATE TABLE tombstones (
  tenant text NOT NULL,
  device text NOT NULL,
  command text NOT NULL,
  digest text NOT NULL,
  status text NOT NULL,
  retain_until epoch_ms NOT NULL,
  PRIMARY KEY(tenant,device,command),
  FOREIGN KEY(tenant,device) REFERENCES devices(tenant,id)
);

-- BASELINE TABLE: inbox; retention: retain across resultretransmission and7day replaywindow; referenced session retained; neverdrop toallow oldeventreexecution
CREATE TABLE inbox (
  tenant text NOT NULL,
  device text NOT NULL,
  event text NOT NULL,
  digest text NOT NULL,
  session text NOT NULL,
  seq bigint NOT NULL,
  reply text,
  PRIMARY KEY(tenant,device,event),
  UNIQUE(session,seq),
  FOREIGN KEY(tenant,device) REFERENCES devices(tenant,id),
  FOREIGN KEY(tenant,device,session) REFERENCES device_sessions(tenant,device,id),
  CHECK(reply IS NULL OR jsonb_typeof(reply::jsonb)='object')
);

-- BASELINE TABLE: outbox; retention: pending/unsettled neverpurge; settled metadata proposed30days withcommand FKcoordination
CREATE TABLE outbox (
  id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  tenant text NOT NULL,
  device text NOT NULL,
  command text NOT NULL,
  kind text NOT NULL,
  state text NOT NULL DEFAULT 'pending',
  UNIQUE(tenant,command,kind),
  FOREIGN KEY(tenant,command) REFERENCES commands(tenant,id),
  FOREIGN KEY(tenant,device,command) REFERENCES commands(tenant,device,id),
  UNIQUE(tenant,id)
);

-- BASELINE TABLE: audit; retention: proposed90day minimum subjecttoowner/privacyapproval; IDs/errorcodes only; immutableappwrites; legalholdpolicy separate
CREATE TABLE audit (
  id text PRIMARY KEY,
  tenant text NOT NULL,
  actor text NOT NULL,
  action text NOT NULL,
  target text NOT NULL,
  outcome text NOT NULL,
  created epoch_ms NOT NULL,
  FOREIGN KEY(tenant) REFERENCES tenants(id)
);

-- BASELINE TABLE: exports; retention: retain approvals whilecommand/artifact referencesexist; expiresblocks new use not deletes history; proposed30days
CREATE TABLE exports (
  tenant text NOT NULL,
  id text NOT NULL,
  actor text NOT NULL,
  scope text NOT NULL,
  digest text NOT NULL,
  reason text NOT NULL,
  state text NOT NULL DEFAULT 'pending',
  approval text,
  approver text,
  expires epoch_ms NOT NULL,
  PRIMARY KEY(tenant,id),
  FOREIGN KEY(tenant,actor) REFERENCES users(tenant,id),
  FOREIGN KEY(tenant,approver) REFERENCES users(tenant,id),
  CHECK(approver IS NULL OR approver!=actor),
  CHECK(scope IS NULL OR jsonb_typeof(scope::jsonb)='object')
);

-- BASELINE TABLE: deletions; retention: retain evidence oflogical deletion and pendingdeviceerase; cannot promisebackup erasure
CREATE TABLE deletions (
  tenant text NOT NULL,
  id text NOT NULL,
  device text NOT NULL,
  state text NOT NULL,
  created epoch_ms NOT NULL,
  PRIMARY KEY(tenant,id),
  FOREIGN KEY(tenant,device) REFERENCES devices(tenant,id)
);

-- BASELINE TABLE: developer_messages; retention: dedupe linkage >=command/tombstone lifetime; no automatic replayafterkeycleanup
CREATE TABLE developer_messages (
  tenant text NOT NULL,
  app text NOT NULL,
  batch text NOT NULL,
  message text NOT NULL,
  child text NOT NULL,
  command text NOT NULL,
  key_hash text NOT NULL,
  request_hash text NOT NULL,
  PRIMARY KEY(tenant,app,message),
  UNIQUE(tenant,app,key_hash),
  UNIQUE(tenant,command),
  FOREIGN KEY(tenant,app) REFERENCES developer_apps(tenant,id),
  FOREIGN KEY(tenant,command) REFERENCES commands(tenant,id)
);

-- BASELINE TABLE: developer_callbacks; retention: pendingdeadletter until explicitlysettled; delivered/deadletter proposed30days; originalbodybytes retainedforaudit/replaypolicy; noauto resendafterrestore
CREATE TABLE developer_callbacks (
  event text PRIMARY KEY,
  tenant text NOT NULL,
  app text NOT NULL,
  command text NOT NULL,
  status text NOT NULL,
  body text NOT NULL,
  state text NOT NULL DEFAULT 'pending',
  attempts bigint NOT NULL DEFAULT 0,
  next_at epoch_ms NOT NULL,
  last_code text,
  UNIQUE(tenant,command,status),
  FOREIGN KEY(tenant,app) REFERENCES developer_apps(tenant,id),
  FOREIGN KEY(tenant,command) REFERENCES commands(tenant,id),
  UNIQUE(tenant,event),
  CHECK(body IS NULL OR jsonb_typeof(body::jsonb)='object')
);

-- Additional same-app binding constraints; stronger than two independent legacy FKs.
ALTER TABLE commands ADD CONSTRAINT commands_app_identity UNIQUE(tenant,app_id,id);
ALTER TABLE developer_messages ADD CONSTRAINT messages_same_app_command
  FOREIGN KEY(tenant,app,command) REFERENCES commands(tenant,app_id,id);
ALTER TABLE developer_callbacks ADD CONSTRAINT callbacks_same_app_command
  FOREIGN KEY(tenant,app,command) REFERENCES commands(tenant,app_id,id);

-- BASELINE query/expiry indexes (all proposed, no measured query plan).
