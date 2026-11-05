"""Import-pure, executor-injected synthetic PostgreSQL registration provisioner.

No CLI, process, network, password discovery, import-time reads or auto-provision.
The trusted executor must authenticate each SQLStep.login_role independently,
use psql -X -qAt -v ON_ERROR_STOP=1, keep SQL/passwords/output private, and report
success only after process exit and clean COMMIT acknowledgement. Never retry.

Proof model: exact reviewed source execution on a new pinned PostgreSQL server,
plus independently source-derived structural contracts and exact function bodies.
Constraint deparser equivalence is NOT independently proven. Observed definitions
are retained privately, never adopted as expected truth. PostgreSQL, bootstrap,
owner, executor and the one-shot supervisor are explicitly trusted.
"""
from __future__ import annotations

from collections import Counter
from dataclasses import dataclass, field
import hashlib
import json
from pathlib import Path
import re
from typing import Protocol

SCHEMA = "wt_synthetic_registry"
OWNER = "wt_registration_lab_owner"
ISSUER = "wt_registration_lab_issuer"
CONSUMER = "wt_registration_lab_consumer"
ROUTINE = "wt_registration_lab_routine"
UNRELATED = "wt_registration_lab_unrelated"
ROLES = (OWNER, ISSUER, CONSUMER, ROUTINE, UNRELATED)
LOGIN_ROLES = (OWNER, ISSUER, CONSUMER, UNRELATED)
CONSUME = "consume_prepared_fresh_registration_v1"
VERSION = 180006
MAX_RESULT_BYTES = 1 << 20
SOURCE_PINS = {
 'backend-go/migrations/0001_base19.sql': 'efc3e7346d7b125a86313c4af518e1dc4443c9ad0f9951fa856178f5ff309fa5',
 'backend-go/migrations/0002_control7.sql': '20d4ab01e8775452bb70fb67b0958c9092f0172f0213afbd009b6b44d2c31bba',
 'backend-go/migrations/0003_admission2.sql': 'ecbdae56cb9de34162896552d6e5ccd32745ffa0dcb45dea4f67945f0ab8c590',
 'backend-go/migrations/0004_admission_constraints.sql': '8654b64911fdfd7a67d964b9f25c8072f1b7f33270f0af11a6320e92023afa9f',
 'backend-go/migrations/0005_indexes_roles.sql': 'df53e271ced0481e96c20d76307084169a4deabf34d6697e7a5df4fbe47e9f1a',
 'backend-go/migrations/0006_commands_payload_json.sql': '87a2c94d09fed84b9e24fb84576cb29044eb7c65b3061a18ac57e37b32f7fe17',
 'backend-go/migrations/0007_gateway_lease_connection.sql': '8e30c5a9775e33a647a654e034913aecb6a2a24b7ebc0c9def4c3d69691e712c',
 'backend-go/migrations/0008_gateway_session_result.sql': 'eaabf4b1d6605590acde493aab141e2efd0649ddf922cb94fead35592838f6c7',
 'backend-go/migrations/0009_callback_destination_versions.sql': '68519d55a29dee075ba5ec541a98de2cfeb7c2e6bdcf6f8396f83bc264cfcfd6',
 'backend-go/migrations/0010_callback_replay_receipts.sql': '0b19aa77527642a9452365bde8631176c9241b2fb682a784e94cf92933d954fd',
 'backend-go/internal/store/pg/testdata/fresh_registration_registry_candidate.sql': '042a674dc4cff90ffbdbb28ce43c29c1d6b1c4378803572a308b516c4daaccfc',
 'backend-go/internal/store/pg/testdata/registration_authority_boundary.sql': '8407530ba3b382ddea3b81ef4bdc4185792a87a9b6d6ac1a6e0e475f962bba6a',
}
REGISTRY_PATH, BOUNDARY_PATH = tuple(SOURCE_PINS)[-2:]


class ProvisionError(RuntimeError):
    """Only fixed bounded codes cross the private execution boundary."""


def require(ok, code):
    if not ok:
        raise ProvisionError(code)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()


@dataclass(frozen=True)
class SQLStep:
    phase: str
    login_role: str
    database: str
    sql: str = field(repr=False)
    query: bool = False
    timeout_seconds: int = 30


@dataclass(frozen=True)
class SourceBundle:
    texts: tuple[tuple[str, str], ...] = field(repr=False)
    sql_manifest_sha256: str
    contract: dict = field(repr=False)


@dataclass(frozen=True)
class InstallReceipt:
    private_metadata: dict = field(repr=False)
    public_evidence: dict
    # Full source/actual constraint pairs and definitions, no passwords or rows.
    private_catalog_receipt: dict = field(repr=False)


class Executor(Protocol):
    def execute(self, step: SQLStep) -> list[dict]: ...


# This lexer is intentionally narrow. It does not claim to parse PostgreSQL SQL.
# It keeps literals/dollar bodies opaque, preserves exact bodies, and refuses
# unclosed quotes/comments. Pins restrict the accepted grammar to reviewed files.
def _pieces(text):
    i = 0
    while i < len(text):
        start = i
        if text.startswith("--", i):
            i = text.find("\n", i)
            if i < 0:
                i = len(text)
            yield "comment", text[start:i]
        elif text.startswith("/*", i):
            end = text.find("*/", i + 2)
            require(end >= 0 and "/*" not in text[i + 2:end], "SOURCE_COMMENT_UNSUPPORTED")
            i = end + 2
            yield "comment", text[start:i]
        elif text[i] in "'\"":
            quote = text[i]
            i += 1
            while i < len(text):
                if text[i] == quote:
                    i += 1
                    if i < len(text) and text[i] == quote:
                        i += 1
                        continue
                    break
                i += 1
            else:
                raise ProvisionError("SOURCE_UNCLOSED_QUOTE")
            yield "literal" if quote == "'" else "identifier", text[start:i]
        elif text[i] == "$" and (match := re.match(r"\$[A-Za-z_0-9]*\$", text[i:])):
            delim = match.group()
            end = text.find(delim, i + len(delim))
            require(end >= 0, "SOURCE_UNCLOSED_DOLLAR")
            i = end + len(delim)
            yield "dollar", text[start:i]
        else:
            i += 1
            yield "char", text[start:i]


def _clean(text):
    return "".join(" " if k == "comment" else v for k, v in _pieces(text)).strip()


def _split(text, delimiter=";"):
    result, current, depth = [], [], 0
    for kind, value in _pieces(text):
        if kind == "comment":
            current.append(" ")
            continue
        if kind == "char":
            if value == "(":
                depth += 1
            elif value == ")":
                depth -= 1
                require(depth >= 0, "SOURCE_PARENTHESIS")
            if value == delimiter and depth == 0:
                if "".join(current).strip():
                    result.append("".join(current).strip())
                current = []
                continue
        current.append(value)
    require(depth == 0, "SOURCE_PARENTHESIS")
    if "".join(current).strip():
        result.append("".join(current).strip())
    return result


def _paren(text, start):
    require(text[start] == "(", "SOURCE_EXPECTED_PARENTHESIS")
    depth, used = 0, start
    for kind, value in _pieces(text[start:]):
        if kind == "char":
            depth += value == "("
            depth -= value == ")"
            if depth == 0:
                return text[start + 1:used], used + 1
        used += len(value)
    raise ProvisionError("SOURCE_PARENTHESIS")


def _name(raw):
    value = raw.strip()
    if value.startswith(SCHEMA + "."):
        value = value[len(SCHEMA) + 1:]
    require(re.fullmatch(r"[a-z][a-z0-9_]*", value) is not None, "SOURCE_IDENTIFIER")
    return value


def _names(raw):
    return tuple(_name(x) for x in raw.split(","))


def _expanded_statements(path, text):
    if path == REGISTRY_PATH:
        chunks = re.findall(r"\$ddl\$(.*?)\$ddl\$, ns\);", text, re.S)
        require(len(chunks) == 13, "SOURCE_REGISTRY_DDL_COUNT")
        statements = [_clean(x.replace("%1$I", SCHEMA).replace("%%", "%")) for x in chunks]
        for table in ("policies", "keys", "bindings", "receipts"):
            statements.append(f"CREATE TRIGGER registration_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON {SCHEMA}.callback_registration_{table} FOR EACH STATEMENT EXECUTE FUNCTION {SCHEMA}.reject_callback_registration_mutation()")
        return statements
    statements = _split(text)
    if path.endswith("0006_commands_payload_json.sql"):
        # Exact literal DDL inside the pinned guarded DO, not arbitrary execution.
        for match in re.finditer(r"EXECUTE pg_catalog.format\('((?:[^']|'')*)',target_schema\)", text):
            statements.append(match.group(1).replace("''", "'").replace("%I", SCHEMA))
        require(len(statements) == 4, "SOURCE_PAYLOAD_DDL_COUNT")
    return statements


def _constraint(table, fragment, column=None):
    explicit = ""
    match = re.match(r"CONSTRAINT\s+(\w+)\s+", fragment, re.I)
    if match:
        explicit = match.group(1)
        fragment = fragment[match.end():]
    c = {"table": table, "name": explicit, "kind": "", "keys": (), "ref_table": "", "ref_keys": (),
         "deferrable": "DEFERRABLE" in fragment and "NOT DEFERRABLE" not in fragment,
         "deferred": "INITIALLY DEFERRED" in fragment, "source": fragment, "expr": ""}
    if fragment.startswith("CHECK"):
        start = fragment.index("(")
        expr, end = _paren(fragment, start)
        require(not fragment[end:].strip(), "SOURCE_CHECK_TAIL")
        c.update(kind="c", expr=expr)
    elif fragment.startswith("PRIMARY KEY") or fragment.startswith("UNIQUE"):
        c["kind"] = "p" if fragment.startswith("PRIMARY KEY") else "u"
        if column is None:
            body, end = _paren(fragment, fragment.index("("))
            c["keys"] = _names(body)
            require(not fragment[end:].strip(), "SOURCE_KEY_TAIL")
        else:
            c["keys"] = (column,)
    elif fragment.startswith("FOREIGN KEY") or fragment.startswith("REFERENCES"):
        c["kind"] = "f"
        if column is None:
            body, end = _paren(fragment, fragment.index("("))
            c["keys"] = _names(body)
            fragment = fragment[end:].strip()
        else:
            c["keys"] = (column,)
        match = re.fullmatch(r"REFERENCES\s+([a-z0-9_.]+)\s*\(([^)]+)\)(?:\s+DEFERRABLE\s+INITIALLY\s+DEFERRED)?", fragment, re.S)
        require(match is not None, "SOURCE_FK_UNSUPPORTED")
        c.update(ref_table=_name(match[1]), ref_keys=_names(match[2]))
    else:
        raise ProvisionError("SOURCE_CONSTRAINT_UNSUPPORTED")
    return c


def _column(table, fragment, contract):
    match = re.match(r"([a-z][a-z0-9_]*)\s+(text\[\]|text|bigint|epoch_ms|boolean|jsonb|timestamptz|integer|numeric\(\d+,\d+\)|bytea|oid|xid8)(?=\s|$)(.*)", fragment, re.S)
    # The word boundary after [] is not meaningful; handle that exact type.
    if match is None:
        match = re.match(r"([a-z][a-z0-9_]*)\s+(text\[\])\s*(.*)", fragment, re.S)
    require(match is not None, "SOURCE_COLUMN_UNSUPPORTED")
    name, typ, tail = match.groups()
    require(name not in contract["tables"][table], "SOURCE_DUPLICATE_COLUMN")
    info = {"type": typ, "not_null": False, "identity": "", "generated": "", "default_source": ""}
    while tail.strip():
        tail = tail.strip()
        if tail.startswith("NOT NULL"):
            info["not_null"] = True
            tail = tail[8:]
        elif tail.startswith("PRIMARY KEY"):
            info["not_null"] = True
            contract["constraints"].append(_constraint(table, "PRIMARY KEY", name))
            tail = tail[11:]
        elif tail.startswith("UNIQUE"):
            contract["constraints"].append(_constraint(table, "UNIQUE", name))
            tail = tail[6:]
        elif tail.startswith("CHECK"):
            _, end = _paren(tail, tail.index("("))
            contract["constraints"].append(_constraint(table, tail[:end]))
            tail = tail[end:]
        elif tail.startswith("REFERENCES"):
            _, end = _paren(tail, tail.index("("))
            contract["constraints"].append(_constraint(table, tail[:end], name))
            tail = tail[end:]
        elif tail.startswith("GENERATED BY DEFAULT AS IDENTITY"):
            info.update(identity="d", not_null=True)
            tail = tail[len("GENERATED BY DEFAULT AS IDENTITY"):]
        elif tail.startswith("GENERATED ALWAYS AS"):
            body, end = _paren(tail, tail.index("("))
            require(tail[end:].strip() == "STORED", "SOURCE_GENERATED_UNSUPPORTED")
            info.update(generated="s", default_source=body)
            tail = ""
        elif tail.startswith("DEFAULT"):
            tail = tail[7:].strip()
            # Pinned defaults are one scalar literal or clock_timestamp().
            match = re.match(r"(clock_timestamp\(\)|false|true|'[^']*'|[0-9]+)(.*)", tail, re.S)
            require(match is not None, "SOURCE_DEFAULT_UNSUPPORTED")
            info["default_source"], tail = match.groups()
        else:
            raise ProvisionError("SOURCE_COLUMN_TAIL")
    contract["tables"][table][name] = info


def derive_contract(texts):
    contract = {"tables": {}, "functions": {}, "triggers": [], "constraints": [], "indexes": [], "domain": "epoch_ms"}
    for path, text in texts:
        for statement in _expanded_statements(path, text):
            match = re.match(r"CREATE TABLE\s+([a-z0-9_.]+)\s*\(", statement)
            if match:
                table = _name(match[1])
                require(table not in contract["tables"], "SOURCE_DUPLICATE_TABLE")
                contract["tables"][table] = {}
                body, end = _paren(statement, statement.index("(", match.start()))
                require(not statement[end:].strip(), "SOURCE_TABLE_TAIL")
                for frag in _split(body, ","):
                    if re.match(r"(?:CONSTRAINT|PRIMARY KEY|UNIQUE|FOREIGN KEY|CHECK)\b", frag):
                        contract["constraints"].append(_constraint(table, frag))
                    else:
                        _column(table, frag, contract)
                continue
            match = re.match(r"ALTER TABLE\s+([a-z0-9_.]+)\s+(.*)", statement, re.S)
            if match:
                table = _name(match[1])
                require(table in contract["tables"], "SOURCE_ALTER_UNKNOWN_TABLE")
                for change in _split(match[2], ","):
                    if change.startswith("ADD COLUMN "):
                        _column(table, change[11:], contract)
                    elif change.startswith("ADD "):
                        contract["constraints"].append(_constraint(table, change[4:]))
                    elif change.startswith("DROP CONSTRAINT "):
                        name = change[16:].strip()
                        old = [c for c in contract["constraints"] if c["table"] == table and (c["name"] == name or (name == f"commands_{c.get('column_hint', '')}_check"))]
                        # Auto-named one-column baseline CHECKs changed by 0006/8.
                        if not old and name in ("commands_payload_check", "commands_result_check"):
                            col = name.split("_")[1]
                            old = [c for c in contract["constraints"] if c["table"] == table and c["kind"] == "c" and c["expr"].lstrip().startswith(col + " IS NULL")]
                        require(len(old) == 1, "SOURCE_DROP_CONSTRAINT_UNKNOWN")
                        contract["constraints"].remove(old[0])
                    else:
                        raise ProvisionError("SOURCE_ALTER_UNSUPPORTED")
                continue
            match = re.match(r"CREATE FUNCTION\s+([a-z0-9_.]+)\s*\((.*?)\)\s*RETURNS\s+(trigger|text)\s+(.*?)\s+AS\s+(\$[A-Za-z_0-9]*\$)(.*)\5\s*$", statement, re.S)
            if match:
                rawname, args, ret, attrs, _, body = match.groups()
                name = _name(rawname)
                require(name not in contract["functions"], "SOURCE_FUNCTION_OVERLOAD")
                argnames = []
                if args.strip():
                    require(name == CONSUME and " ".join(args.split()) == "p_grant_id text, p_operation_id text", "SOURCE_FUNCTION_ARGS")
                    argnames = ["p_grant_id", "p_operation_id"]
                fixed_path = "SET search_path" in attrs or path.endswith(("0009_callback_destination_versions.sql", "0010_callback_replay_receipts.sql"))
                config = [f"search_path=pg_catalog, {SCHEMA}, pg_temp"] if fixed_path else []
                if name == CONSUME:
                    config += ["row_security=on", "lock_timeout=1s"]
                contract["functions"][name] = {"body": body, "args": ["text"] * len(argnames), "argnames": argnames, "result": ret,
                    "owner": ROUTINE if name == CONSUME else OWNER, "security_definer": name == CONSUME,
                    "config": config}
                continue
            match = re.fullmatch(r"CREATE TRIGGER\s+(\w+)\s+(BEFORE|AFTER)\s+([A-Z ]+)\s+ON\s+([a-z0-9_.]+)\s+FOR EACH (ROW|STATEMENT) EXECUTE FUNCTION\s+([a-z0-9_.]+)\(\)", " ".join(statement.split()))
            if match:
                name, timing, events, table, level, fn = match.groups()
                bits = (2 if timing == "BEFORE" else 0) + (1 if level == "ROW" else 0)
                for event in events.split(" OR "):
                    require(event in ("INSERT", "UPDATE", "DELETE", "TRUNCATE"), "SOURCE_TRIGGER_EVENT")
                    bits += {"INSERT": 4, "DELETE": 8, "UPDATE": 16, "TRUNCATE": 32}[event]
                contract["triggers"].append({"table": _name(table), "name": name, "function": _name(fn), "kind": bits})
                continue
            match = re.fullmatch(r"CREATE (UNIQUE )?INDEX\s+(\w+)\s+ON\s+(\w+)\s*\(([^)]+)\)(?:\s+WHERE\s+(.+))?", statement, re.S)
            if match:
                unique, name, table, cols, predicate = match.groups()
                keys, options = [], []
                for col in cols.split(","):
                    words = col.strip().split()
                    require(len(words) == 1 or len(words) == 2 and words[1] == "DESC", "SOURCE_INDEX_UNSUPPORTED")
                    keys.append(_name(words[0])); options.append(3 if len(words) == 2 else 0)
                contract["indexes"].append({"name": name, "table": table, "keys": tuple(keys), "options": tuple(options), "unique": bool(unique), "predicate_source": predicate or ""})
                continue
            # Statements handled by the exact trusted execution transcript. No
            # unrecognized DDL is silently ignored by this inventory extractor.
            require(not re.match(r"(?:CREATE|ALTER|DROP)\b", statement) or statement.startswith(("CREATE DOMAIN epoch_ms AS bigint CHECK", "ALTER FUNCTION ")), "SOURCE_DDL_UNSUPPORTED")
    for c in contract["constraints"]:
        if c["kind"] == "p":
            for col in c["keys"]:
                contract["tables"][c["table"]][col]["not_null"] = True
        if c["kind"] == "c":
            visible = "".join(v if k not in ("literal", "comment") else " " for k, v in _pieces(c["expr"]))
            c["keys"] = tuple(sorted(set(re.findall(r"\b[a-z][a-z0-9_]*\b", visible)) & set(contract["tables"][c["table"]])))
    require(len(contract["tables"]) == 37 and len(contract["functions"]) == 20 and len(contract["triggers"]) == 24, "SOURCE_INVENTORY_COUNT")
    contract["acls"] = _derive_acls(dict(texts)[BOUNDARY_PATH], contract)
    return contract


def _derive_acls(boundary, contract):
    # Non-owner exact grant multiset. Owner intrinsic rights are checked separately.
    result = []
    for role in (ISSUER, CONSUMER, UNRELATED):
        result.append(("database", "", "", role, "CONNECT"))
    for role in (ISSUER, CONSUMER, ROUTINE, UNRELATED):
        result.append(("schema", SCHEMA, "", role, "USAGE"))
    result.append(("function", CONSUME, "", CONSUMER, "EXECUTE"))
    for statement in _split(boundary):
        match = re.fullmatch(r"GRANT (SELECT|INSERT|UPDATE|EXECUTE)(?:\s*\(([^)]+)\))?\s+ON\s+(FUNCTION\s+)?(.+?)\s+TO\s+(wt_registration_lab_\w+)", statement, re.S)
        if not match:
            continue
        priv, columns, function, targets, role = match.groups()
        if function and CONSUME in targets:
            continue  # One final grant above; ownership/temporary changes excluded.
        for target in targets.split(","):
            name = _name(target.strip().removesuffix("()"))
            if columns:
                for col in _names(columns):
                    require(col in contract["tables"][name], "SOURCE_ACL_COLUMN")
                    result.append(("column", name, col, role, priv))
            else:
                result.append(("function" if function else "table", name, "", role, priv))
    require(len(result) == len(set(result)), "SOURCE_DUPLICATE_ACL")
    return sorted(result)


def load_and_verify_sources(source_root):
    root = Path(source_root)
    require(root.is_dir() and not root.is_symlink(), "SOURCE_ROOT_INVALID")
    texts = []
    for relative, digest in SOURCE_PINS.items():
        p = root / relative
        require(not any(x.is_symlink() for x in (p, *p.parents)) and p.is_file(), "SOURCE_PATH_INVALID")
        body = p.read_bytes()
        require(len(body) <= 1 << 20 and sha(body) == digest, "SOURCE_HASH_MISMATCH")
        try:
            text = body.decode("utf-8")
        except UnicodeError:
            raise ProvisionError("SOURCE_ENCODING") from None
        require("\x00" not in text and "\r" not in text, "SOURCE_ENCODING")
        texts.append((relative, text))
    digest = sha(b"".join(p.encode() + b"\0" + SOURCE_PINS[p].encode() + b"\n" for p in sorted(SOURCE_PINS)))
    return SourceBundle(tuple(texts), digest, derive_contract(tuple(texts)))


def _verified_bundle(bundle):
    require(isinstance(bundle, SourceBundle) and tuple(p for p, _ in bundle.texts) == tuple(SOURCE_PINS), "SOURCE_BUNDLE_INVALID")
    for p, text in bundle.texts:
        require(sha(text.encode()) == SOURCE_PINS[p], "SOURCE_HASH_MISMATCH")
    require(bundle.contract == derive_contract(bundle.texts), "SOURCE_CONTRACT_MISMATCH")


def _validate_target(database, bootstrap_role):
    require(isinstance(database, str) and re.fullmatch(r"wt_test_[a-z0-9_]{8,48}", database), "TARGET_DATABASE_INVALID")
    require(isinstance(bootstrap_role, str) and re.fullmatch(r"wt_admin_[a-z0-9_]{16,40}", bootstrap_role), "BOOTSTRAP_ROLE_INVALID")


def _quote(value):
    require(isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9_-]{32,128}", value), "PASSWORD_FORMAT_INVALID")
    return "'" + value + "'"


def build_provision_plan(bundle, database, bootstrap_role, role_passwords):
    _verified_bundle(bundle)
    _validate_target(database, bootstrap_role)
    require(type(role_passwords) is dict and set(role_passwords) == set(LOGIN_ROLES), "PASSWORD_ROLE_SET_INVALID")
    require(all(type(value) is str for value in role_passwords.values()), "PASSWORD_FORMAT_INVALID")
    require(len(set(role_passwords.values())) == 4, "PASSWORDS_NOT_DISTINCT")
    roles_sql = "BEGIN;\nSET LOCAL password_encryption='scram-sha-256';\n"
    for role in ROLES:
        password = " PASSWORD " + _quote(role_passwords[role]) if role in LOGIN_ROLES else ""
        roles_sql += f"CREATE ROLE {role} {'LOGIN' if role in LOGIN_ROLES else 'NOLOGIN'} NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT CONNECTION LIMIT 2{password};\n"
    roles_sql += f"GRANT {ROUTINE} TO {OWNER} WITH ADMIN FALSE, INHERIT FALSE, SET TRUE;\nCOMMIT;\n"
    steps = [SQLStep("bootstrap_preflight", bootstrap_role, "postgres", _bootstrap_query(database, bootstrap_role), True),
             SQLStep("bootstrap_roles", bootstrap_role, "postgres", roles_sql),
             SQLStep("bootstrap_database", bootstrap_role, "postgres", f"CREATE DATABASE {database} OWNER {OWNER} TEMPLATE template0 ENCODING 'UTF8';\n"),
             SQLStep("owner_schema", OWNER, database, f"BEGIN;\n{_session_guard(database, OWNER)}\nCREATE SCHEMA {SCHEMA} AUTHORIZATION {OWNER};\nREVOKE ALL ON SCHEMA {SCHEMA} FROM PUBLIC;\nCOMMIT;\n")]
    for i, (path, sql) in enumerate(bundle.texts):
        if path == BOUNDARY_PATH:
            # Entire exact script, own BEGIN/COMMIT; never wrap in another tx.
            wrapped = _session_guard(database, OWNER) + "\n" + sql
            phase = "boundary"
        else:
            wrapped = f"BEGIN;\nSET LOCAL search_path TO {SCHEMA}, pg_catalog, pg_temp;\n{_session_guard(database, OWNER)}\n" + sql + "\nCOMMIT;\n"
            phase = "registry" if path == REGISTRY_PATH else f"migration_{i + 1:04d}"
        steps.append(SQLStep(phase, OWNER, database, wrapped))
    steps.append(SQLStep("synthetic_seed", OWNER, database, seed_sql(database)))
    steps.append(SQLStep("catalog_readback", OWNER, database, catalog_query(bundle), True))
    return tuple(steps)


def _session_guard(database, role):
    return f"""DO $identity$ BEGIN
 IF session_user <> '{role}' OR current_user <> session_user
 OR pg_catalog.current_database() <> '{database}'
 OR pg_catalog.current_setting('server_version_num') <> '180006'
 OR EXISTS (SELECT 1 FROM pg_catalog.pg_stat_activity
    WHERE pid<>pg_catalog.pg_backend_pid() AND (backend_type='client backend' OR usename IS NOT NULL OR backend_type IS NULL))
 THEN RAISE EXCEPTION 'REGISTRATION_INSTALL_SESSION_INVALID'; END IF;
 END $identity$;"""


def _bootstrap_query(database, role):
    return f"""SELECT pg_catalog.json_build_array(pg_catalog.json_build_object(
 'version',pg_catalog.current_setting('server_version_num')::integer,
 'session_user',session_user,'current_user',current_user,
 'database_absent',NOT EXISTS(SELECT 1 FROM pg_catalog.pg_database WHERE datname='{database}'),
 'roles_absent',NOT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname IN ({','.join(repr(x) for x in ROLES)})),
 'other_clients',(SELECT count(*) FROM pg_catalog.pg_stat_activity WHERE pid<>pg_catalog.pg_backend_pid() AND (backend_type='client backend' OR usename IS NOT NULL OR backend_type IS NULL)),
 'bootstrap_superuser',(SELECT rolsuper FROM pg_catalog.pg_roles WHERE rolname='{role}'),
 'event_triggers',(SELECT count(*) FROM pg_catalog.pg_event_trigger)
 ));"""


def seed_sql(database):
    return f"""BEGIN;
SET LOCAL search_path TO {SCHEMA}, pg_catalog, pg_temp;
{_session_guard(database, OWNER)}
INSERT INTO {SCHEMA}.tenants(id) VALUES('tenant_synthetic');
INSERT INTO {SCHEMA}.users(tenant,id,login,role,password,active)
 VALUES('tenant_synthetic','actor_synthetic','actor_synthetic','owner','!disabled-inert-synthetic-not-a-password!',false);
INSERT INTO {SCHEMA}.developer_apps(tenant,id,creator,name,token_hash,active,auth_epoch,token_expires,scopes,callback_url)
 VALUES('tenant_synthetic','app_synthetic','actor_synthetic','Synthetic registration fixture','!inert-synthetic-no-auth-token!',true,0,0,'[]'::jsonb,NULL);
INSERT INTO {SCHEMA}.scope_limits(scope_kind,tenant,scope_id,destination_id,max_in_flight,weight,enabled,rate_per_sec,burst,tokens,refill_at,blocked_until,virtual_finish,policy_version,capacity_jobs,capacity_bytes,charged_jobs,charged_bytes,admission_enabled,admission_policy_version)
 VALUES('tenant','tenant_synthetic','tenant_synthetic',NULL,1,1,false,1,1,0,pg_catalog.clock_timestamp(),NULL,0,1,0,0,0,0,false,1);
COMMIT;
"""


def catalog_query(bundle):
    """One bounded JSON array, only catalog metadata and exact synthetic booleans."""
    _verified_bundle(bundle)
    rows = []
    def add(kind, fields, source):
        pairs = ["'kind'", repr(kind)]
        for key, expression in fields.items():
            pairs.extend((repr(key), expression))
        rows.append("SELECT pg_catalog.jsonb_build_object(" + ",".join(pairs) + ") AS item " + source)
    ns = f"'{SCHEMA}'::pg_catalog.regnamespace"
    rolelist = ",".join(repr(x) for x in ROLES)
    add("identity", {
        "database":"pg_catalog.current_database()", "database_oid":"d.oid", "database_owner":"pg_catalog.pg_get_userbyid(d.datdba)",
        "schema_oid":"n.oid", "schema_owner":"pg_catalog.pg_get_userbyid(n.nspowner)",
        "version":"pg_catalog.current_setting('server_version_num')::integer", "session_user":"session_user", "current_user":"current_user",
        "other_clients":"(SELECT count(*) FROM pg_catalog.pg_stat_activity WHERE pid<>pg_catalog.pg_backend_pid() AND (backend_type='client backend' OR usename IS NOT NULL OR backend_type IS NULL))",
        "unexpected_schemas":f"(SELECT count(*) FROM pg_catalog.pg_namespace WHERE nspname NOT IN ('pg_catalog','information_schema','public','{SCHEMA}') AND nspname !~ '^pg_toast' AND nspname !~ '^pg_temp_')",
        "public_objects":"(SELECT count(*) FROM pg_catalog.pg_class WHERE relnamespace='public'::pg_catalog.regnamespace)+(SELECT count(*) FROM pg_catalog.pg_proc WHERE pronamespace='public'::pg_catalog.regnamespace)",
        "default_acls":"(SELECT count(*) FROM pg_catalog.pg_default_acl)", "role_settings":"(SELECT count(*) FROM pg_catalog.pg_db_role_setting)",
        "event_triggers":"(SELECT count(*) FROM pg_catalog.pg_event_trigger)",
        "extensions":"(SELECT pg_catalog.array_agg(extname ORDER BY extname) FROM pg_catalog.pg_extension)",
        "rewrites":f"(SELECT count(*) FROM pg_catalog.pg_rewrite r JOIN pg_catalog.pg_class c ON c.oid=r.ev_class WHERE c.relnamespace={ns})",
        "policies":f"(SELECT count(*) FROM pg_catalog.pg_policy p JOIN pg_catalog.pg_class c ON c.oid=p.polrelid WHERE c.relnamespace={ns})",
    },f"FROM pg_catalog.pg_database d,pg_catalog.pg_namespace n WHERE d.datname=pg_catalog.current_database() AND n.oid={ns}")
    add("role", {"name":"r.rolname", "oid":"r.oid", "superuser":"r.rolsuper", "createdb":"r.rolcreatedb", "createrole":"r.rolcreaterole", "replication":"r.rolreplication", "bypassrls":"r.rolbypassrls", "inherit":"r.rolinherit", "login":"r.rolcanlogin", "connection_limit":"r.rolconnlimit", "valid_until":"r.rolvaliduntil", "config":"r.rolconfig"}, f"FROM pg_catalog.pg_roles r WHERE r.rolname IN ({rolelist})")
    add("membership", {"member":"mbr.rolname", "role":"r.rolname", "admin":"m.admin_option", "inherit":"m.inherit_option", "set":"m.set_option"}, f"FROM pg_catalog.pg_auth_members m JOIN pg_catalog.pg_roles r ON r.oid=m.roleid JOIN pg_catalog.pg_roles mbr ON mbr.oid=m.member WHERE r.rolname IN ({rolelist}) OR mbr.rolname IN ({rolelist})")
    add("relation", {"name":"c.relname", "relation_kind":"c.relkind", "owner":"pg_catalog.pg_get_userbyid(c.relowner)", "persistence":"c.relpersistence", "rls":"c.relrowsecurity", "force_rls":"c.relforcerowsecurity", "partition":"c.relispartition", "options":"c.reloptions"}, f"FROM pg_catalog.pg_class c WHERE c.relnamespace={ns}")
    add("column", {"table":"c.relname", "name":"a.attname", "position":"a.attnum", "type":"pg_catalog.format_type(a.atttypid,a.atttypmod)", "not_null":"a.attnotnull", "identity":"a.attidentity", "generated":"a.attgenerated", "default":"pg_catalog.pg_get_expr(d.adbin,d.adrelid)", "dropped":"a.attisdropped", "collation":"co.collname"}, f"FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum LEFT JOIN pg_catalog.pg_collation co ON co.oid=a.attcollation WHERE c.relnamespace={ns} AND c.relkind='r' AND a.attnum>0")
    add("function", {"name":"p.proname", "oid":"p.oid", "owner":"pg_catalog.pg_get_userbyid(p.proowner)", "body":"p.prosrc", "definition":"pg_catalog.pg_get_functiondef(p.oid)", "arguments":"pg_catalog.oidvectortypes(p.proargtypes)", "argument_names":"p.proargnames", "argument_modes":"p.proargmodes", "argument_defaults":"p.pronargdefaults", "result":"pg_catalog.format_type(p.prorettype,NULL)", "security_definer":"p.prosecdef", "language":"l.lanname", "volatility":"p.provolatile", "parallel":"p.proparallel", "strict":"p.proisstrict", "leakproof":"p.proleakproof", "function_kind":"p.prokind", "set_returning":"p.proretset", "config":"p.proconfig", "support":"p.prosupport::oid", "variadic":"p.provariadic::oid", "cost":"p.procost", "rows":"p.prorows", "binary":"p.probin"}, f"FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_language l ON l.oid=p.prolang WHERE p.pronamespace={ns}")
    add("trigger", {"table":"c.relname", "name":"t.tgname", "function":"p.proname", "function_schema":"pn.nspname", "trigger_kind":"t.tgtype", "enabled":"t.tgenabled", "arguments":"t.tgnargs", "argument_bytes":"pg_catalog.encode(t.tgargs,'hex')", "qualification":"t.tgqual::text", "columns":"t.tgattr::text", "constraint":"t.tgconstraint::oid", "deferrable":"t.tgdeferrable", "deferred":"t.tginitdeferred", "old_table":"t.tgoldtable", "new_table":"t.tgnewtable"}, f"FROM pg_catalog.pg_trigger t JOIN pg_catalog.pg_class c ON c.oid=t.tgrelid JOIN pg_catalog.pg_proc p ON p.oid=t.tgfoid JOIN pg_catalog.pg_namespace pn ON pn.oid=p.pronamespace WHERE c.relnamespace={ns} AND NOT t.tgisinternal")
    add("internal_trigger", {"table":"r.relname", "constraint_table":"cr.relname", "constraint_name":"c.conname", "function":"p.proname", "function_schema":"pn.nspname", "trigger_kind":"t.tgtype", "enabled":"t.tgenabled", "deferrable":"t.tgdeferrable", "deferred":"t.tginitdeferred", "arguments":"t.tgnargs", "argument_bytes":"pg_catalog.encode(t.tgargs,'hex')", "qualification":"t.tgqual::text", "columns":"t.tgattr::text"}, f"FROM pg_catalog.pg_trigger t JOIN pg_catalog.pg_class r ON r.oid=t.tgrelid LEFT JOIN pg_catalog.pg_constraint c ON c.oid=t.tgconstraint LEFT JOIN pg_catalog.pg_class cr ON cr.oid=c.conrelid JOIN pg_catalog.pg_proc p ON p.oid=t.tgfoid JOIN pg_catalog.pg_namespace pn ON pn.oid=p.pronamespace WHERE r.relnamespace={ns} AND t.tgisinternal")
    key_sql = "ARRAY(SELECT a.attname FROM unnest(c.conkey) WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.num ORDER BY k.ord)"
    ref_sql = "ARRAY(SELECT a.attname FROM unnest(c.confkey) WITH ORDINALITY k(num,ord) JOIN pg_catalog.pg_attribute a ON a.attrelid=c.confrelid AND a.attnum=k.num ORDER BY k.ord)"
    add("constraint", {"table":"COALESCE(r.relname,'@'||ty.typname)", "name":"c.conname", "constraint_kind":"c.contype", "keys":key_sql, "ref_table":"rr.relname", "ref_schema":"rn.nspname", "ref_keys":ref_sql, "deferrable":"c.condeferrable", "deferred":"c.condeferred", "validated":"c.convalidated", "enforced":"c.conenforced", "local":"c.conislocal", "inherit_count":"c.coninhcount", "no_inherit":"c.connoinherit", "match":"c.confmatchtype", "update_action":"c.confupdtype", "delete_action":"c.confdeltype", "definition":"pg_catalog.pg_get_constraintdef(c.oid,false)", "index":"ix.relname"}, f"FROM pg_catalog.pg_constraint c LEFT JOIN pg_catalog.pg_class r ON r.oid=c.conrelid LEFT JOIN pg_catalog.pg_type ty ON ty.oid=c.contypid LEFT JOIN pg_catalog.pg_class rr ON rr.oid=c.confrelid LEFT JOIN pg_catalog.pg_namespace rn ON rn.oid=rr.relnamespace LEFT JOIN pg_catalog.pg_class ix ON ix.oid=c.conindid WHERE c.connamespace={ns}")
    add("index", {"name":"ix.relname", "table":"c.relname", "keys":"ARRAY(SELECT a.attname FROM unnest(i.indkey) WITH ORDINALITY k(num,ord) LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum=k.num ORDER BY k.ord)", "options":"i.indoption::text", "unique":"i.indisunique", "primary":"i.indisprimary", "valid":"i.indisvalid", "ready":"i.indisready", "live":"i.indislive", "nulls_not_distinct":"i.indnullsnotdistinct", "key_count":"i.indnkeyatts", "attribute_count":"i.indnatts", "method":"am.amname", "expressions":"pg_catalog.pg_get_expr(i.indexprs,i.indrelid)", "predicate":"pg_catalog.pg_get_expr(i.indpred,i.indrelid)", "definition":"pg_catalog.pg_get_indexdef(i.indexrelid)"}, f"FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indrelid JOIN pg_catalog.pg_class ix ON ix.oid=i.indexrelid JOIN pg_catalog.pg_am am ON am.oid=ix.relam WHERE c.relnamespace={ns}")
    add("type", {"name":"t.typname", "type_kind":"t.typtype", "owner":"pg_catalog.pg_get_userbyid(t.typowner)", "relation":"c.relname", "element":"e.typname", "base_type":"b.typname", "not_null":"t.typnotnull", "default":"t.typdefault"}, f"FROM pg_catalog.pg_type t LEFT JOIN pg_catalog.pg_class c ON c.oid=t.typrelid LEFT JOIN pg_catalog.pg_type e ON e.oid=t.typelem LEFT JOIN pg_catalog.pg_type b ON b.oid=t.typbasetype WHERE t.typnamespace={ns}")
    add("sequence", {"name":"c.relname", "data_type":"pg_catalog.format_type(s.seqtypid,NULL)", "start":"s.seqstart", "increment":"s.seqincrement", "min":"s.seqmin", "max":"s.seqmax", "cache":"s.seqcache", "cycle":"s.seqcycle", "table":"o.relname", "column":"a.attname", "dependency":"d.deptype"}, f"FROM pg_catalog.pg_sequence s JOIN pg_catalog.pg_class c ON c.oid=s.seqrelid LEFT JOIN pg_catalog.pg_depend d ON d.classid='pg_catalog.pg_class'::pg_catalog.regclass AND d.objid=c.oid AND d.refclassid='pg_catalog.pg_class'::pg_catalog.regclass AND d.deptype IN ('a','i') LEFT JOIN pg_catalog.pg_class o ON o.oid=d.refobjid LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid=o.oid AND a.attnum=d.refobjsubid WHERE c.relnamespace={ns}")
    # ACL rows include owner rights; validator independently enumerates nonowners.
    for scope, object_expr, column_expr, owner_expr, acl_expr, source in [
        ("table", "c.relname", "''", "c.relowner", "COALESCE(c.relacl,pg_catalog.acldefault('r',c.relowner))", f"pg_catalog.pg_class c WHERE c.relnamespace={ns} AND c.relkind='r'"),
        ("sequence", "c.relname", "''", "c.relowner", "COALESCE(c.relacl,pg_catalog.acldefault('s',c.relowner))", f"pg_catalog.pg_class c WHERE c.relnamespace={ns} AND c.relkind='S'"),
        ("function", "p.proname", "''", "p.proowner", "COALESCE(p.proacl,pg_catalog.acldefault('f',p.proowner))", f"pg_catalog.pg_proc p WHERE p.pronamespace={ns}"),
        ("schema", "n.nspname", "''", "n.nspowner", "COALESCE(n.nspacl,pg_catalog.acldefault('n',n.nspowner))", f"pg_catalog.pg_namespace n WHERE n.oid={ns}"),
        ("database", "''", "''", "d.datdba", "COALESCE(d.datacl,pg_catalog.acldefault('d',d.datdba))", "pg_catalog.pg_database d WHERE d.datname=pg_catalog.current_database()"),
        ("column", "c.relname", "a.attname", "c.relowner", "a.attacl", f"pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid WHERE c.relnamespace={ns} AND a.attnum>0"),
    ]:
        from_sql, where_sql = source.split(" WHERE ", 1)
        add("acl", {"scope":repr(scope), "object":object_expr, "column":column_expr, "owner":f"pg_catalog.pg_get_userbyid({owner_expr})", "grantee":"CASE WHEN acl.grantee=0 THEN 'PUBLIC' ELSE pg_catalog.pg_get_userbyid(acl.grantee) END", "grantor":"pg_catalog.pg_get_userbyid(acl.grantor)", "privilege":"acl.privilege_type", "grantable":"acl.is_grantable"}, f"FROM {from_sql} CROSS JOIN LATERAL pg_catalog.aclexplode({acl_expr}) acl WHERE {where_sql}")
    for table in bundle.contract["tables"]:
        add("row_count", {"table":repr(table), "count":"count(*)"}, f"FROM {SCHEMA}.{table}")
    add("seed", {"exact":f"""(SELECT count(*)=1 FROM {SCHEMA}.tenants WHERE id='tenant_synthetic')
 AND (SELECT count(*)=1 FROM {SCHEMA}.users WHERE tenant='tenant_synthetic' AND id='actor_synthetic' AND login='actor_synthetic' AND role='owner' AND password='!disabled-inert-synthetic-not-a-password!' AND NOT active)
 AND (SELECT count(*)=1 FROM {SCHEMA}.developer_apps WHERE tenant='tenant_synthetic' AND id='app_synthetic' AND creator='actor_synthetic' AND name='Synthetic registration fixture' AND token_hash='!inert-synthetic-no-auth-token!' AND active AND auth_epoch=0 AND token_expires=0 AND scopes='[]'::jsonb AND callback_url IS NULL)
 AND (SELECT count(*)=1 FROM {SCHEMA}.scope_limits WHERE scope_kind='tenant' AND tenant='tenant_synthetic' AND scope_id='tenant_synthetic' AND destination_id IS NULL AND max_in_flight=1 AND weight=1 AND NOT enabled AND rate_per_sec=1 AND burst=1 AND tokens=0 AND blocked_until IS NULL AND virtual_finish=0 AND policy_version=1 AND capacity_jobs=0 AND capacity_bytes=0 AND charged_jobs=0 AND charged_bytes=0 AND NOT admission_enabled AND admission_policy_version=1)"""}, "")
    return "SET search_path TO pg_catalog;\nSELECT COALESCE(pg_catalog.jsonb_agg(item),'[]'::jsonb) FROM (\n" + "\nUNION ALL\n".join(rows) + "\n) AS bounded_catalog;\n"


FIELDS = {
 'identity': 'database database_oid database_owner schema_oid schema_owner version session_user current_user other_clients unexpected_schemas public_objects default_acls role_settings event_triggers extensions rewrites policies',
 'role': 'name oid superuser createdb createrole replication bypassrls inherit login connection_limit valid_until config',
 'membership': 'member role admin inherit set',
 'relation': 'name relation_kind owner persistence rls force_rls partition options',
 'column': 'table name position type not_null identity generated default dropped collation',
 'function': 'name oid owner body definition arguments argument_names argument_modes argument_defaults result security_definer language volatility parallel strict leakproof function_kind set_returning config support variadic cost rows binary',
 'trigger': 'table name function function_schema trigger_kind enabled arguments argument_bytes qualification columns constraint deferrable deferred old_table new_table',
 'internal_trigger': 'table constraint_table constraint_name function function_schema trigger_kind enabled deferrable deferred arguments argument_bytes qualification columns',
 'constraint': 'table name constraint_kind keys ref_table ref_schema ref_keys deferrable deferred validated enforced local inherit_count no_inherit match update_action delete_action definition index',
 'index': 'name table keys options unique primary valid ready live nulls_not_distinct key_count attribute_count method expressions predicate definition',
 'type': 'name type_kind owner relation element base_type not_null default',
 'sequence': 'name data_type start increment min max cache cycle table column dependency',
 'acl': 'scope object column owner grantee grantor privilege grantable',
 'row_count': 'table count',
 'seed': 'exact',
}


def _bounded_rows(raw):
    require(type(raw) is list and 1 <= len(raw) <= 4096, "CATALOG_RESULT_SHAPE")
    try:
        require(len(canonical(raw)) <= MAX_RESULT_BYTES, "CATALOG_RESULT_TOO_LARGE")
    except (TypeError, ValueError, OverflowError):
        raise ProvisionError("CATALOG_RESULT_ENCODING") from None
    groups = {kind: [] for kind in FIELDS}
    for row in raw:
        require(type(row) is dict and row.get("kind") in FIELDS, "CATALOG_ROW_KIND")
        kind = row["kind"]
        require(set(row) == set(FIELDS[kind].split()) | {"kind"}, "CATALOG_ROW_FIELDS")
        groups[kind].append(row)
    return groups


def _one(rows, code):
    require(len(rows) == 1, code)
    return rows[0]


def _oid(value):
    return type(value) is int and 0 < value < 2**32


def _bounded_definition(value):
    return type(value) is str and 1 <= len(value.encode()) <= 65536 and "\x00" not in value


def _object_name(first, second, label):
    # PostgreSQL makeObjectName, NAMEDATALEN=64, ASCII reviewed identifiers.
    first_len, second_len = len(first), len(second)
    available = 63 - len(label) - 1 - (1 if second else 0)
    while first_len + second_len > available:
        if first_len > second_len:
            first_len -= 1
        else:
            second_len -= 1
    return first[:first_len] + ("_" + second[:second_len] if second else "") + "_" + label


def expected_constraints(contract):
    result = [dict(c) for c in contract["constraints"]]
    for table, cols in contract["tables"].items():
        for name, info in cols.items():
            if info["not_null"]:
                result.append({"table":table,"kind":"n","keys":(name,),"ref_table":"","ref_keys":(),"deferrable":False,"deferred":False,"name":"","source":"NOT NULL","expr":""})
    result.append({"table":"@epoch_ms","kind":"c","keys":(),"ref_table":"","ref_keys":(),"deferrable":False,"deferred":False,"name":"","source":"CHECK(VALUE>=0 AND VALUE<=9007199254740991)","expr":"VALUE>=0 AND VALUE<=9007199254740991"})
    used = set()
    for c in result:
        if c["name"]:
            name = c["name"]
        else:
            label = {"p":"pkey","u":"key","f":"fkey","c":"check","n":"not_null"}[c["kind"]]
            columns = c["keys"] if c["kind"] in ("u","f","n") else c["keys"] if c["kind"] == "c" and len(c["keys"]) == 1 else ()
            addition = "_".join(columns)
            counter = 0
            while True:
                name = _object_name(c["table"].removeprefix("@"), addition, label + (str(counter) if counter else ""))
                if (c["table"],name) not in used:
                    break
                counter += 1
        require((c["table"],name) not in used, "SOURCE_CONSTRAINT_NAME_COLLISION")
        used.add((c["table"],name))
        c["catalog_name"] = name
    return result


def _constraint_key(c, actual=False):
    if actual:
        return (c["table"], c["constraint_kind"], tuple(sorted(c["keys"])) if c["constraint_kind"] == 'c' else tuple(c["keys"]), c["ref_table"] or "", tuple(c["ref_keys"]), c["deferrable"], c["deferred"])
    return (c["table"], c["kind"], c["keys"], c["ref_table"], c["ref_keys"], c["deferrable"], c["deferred"])


def validate_catalog(raw, bundle, database, execution_receipt):
    """Validate source-derived contracts, requiring the exact install receipt.

    Not a standalone approval API. Caller must use provision() with its trusted
    injected executor and new-resource supervisor. Constraint/default/index
    expression text is recorded, not silently normalized or claimed equivalent.
    """
    _verified_bundle(bundle)
    require(type(execution_receipt) is list and [x.get("phase") for x in execution_receipt] ==
        ["bootstrap_preflight", "bootstrap_roles", "bootstrap_database", "owner_schema"] +
        [f"migration_{i:04d}" for i in range(1, 11)] + ["registry", "boundary", "synthetic_seed", "catalog_readback"], "INSTALL_RECEIPT_ORDER")
    require(all(set(x) == {"phase", "login_role", "database", "sql_sha256", "acknowledged"} and x["acknowledged"] is True and re.fullmatch(r"[0-9a-f]{64}", x["sql_sha256"]) for x in execution_receipt), "INSTALL_RECEIPT_INVALID")
    require(all(x["login_role"] == OWNER and x["database"] == database for x in execution_receipt[3:]), "INSTALL_OWNER_AUTHENTICATION")
    bootstrap = execution_receipt[0]["login_role"]
    _validate_target(database, bootstrap)
    reference = build_provision_plan(bundle, database, bootstrap, {r:"notused_" + str(i)*40 for i,r in enumerate(LOGIN_ROLES)})
    for i, (observed, expected) in enumerate(zip(execution_receipt, reference)):
        require(observed["login_role"] == expected.login_role and observed["database"] == expected.database, "INSTALL_RECEIPT_TARGET")
        if i != 1:  # Password-bearing bootstrap SQL hash is bound at execution.
            require(observed["sql_sha256"] == sha(expected.sql.encode()), "INSTALL_RECEIPT_SOURCE")
    groups = _bounded_rows(raw)
    contract = bundle.contract
    identity = _one(groups["identity"], "CATALOG_IDENTITY_COUNT")
    require(identity["database"] == database and identity["version"] == VERSION and identity["database_owner"] == OWNER and identity["schema_owner"] == OWNER and identity["session_user"] == OWNER and identity["current_user"] == OWNER and _oid(identity["database_oid"]) and _oid(identity["schema_oid"]), "CATALOG_IDENTITY")
    require(identity["extensions"] == ["plpgsql"] and all(identity[x] == 0 for x in ("other_clients", "unexpected_schemas", "public_objects", "default_acls", "role_settings", "event_triggers", "rewrites", "policies")), "CATALOG_UNEXPECTED_POSTURE")
    require(len(groups["role"]) == 5 and {r["name"] for r in groups["role"]} == set(ROLES), "CATALOG_ROLE_INVENTORY")
    role_oids = {}
    for r in groups["role"]:
        require(_oid(r["oid"]) and r["login"] is (r["name"] != ROUTINE) and r["connection_limit"] == 2 and r["valid_until"] is None and r["config"] is None and all(r[x] is False for x in ("superuser", "createdb", "createrole", "replication", "bypassrls", "inherit")), "CATALOG_ROLE_ATTRIBUTES")
        role_oids[r["name"]] = r["oid"]
    require(len(set(role_oids.values())) == 5, "CATALOG_ROLE_OIDS")
    require(groups["membership"] == [{"kind": "membership", "member": OWNER, "role": ROUTINE, "admin": False, "inherit": False, "set": True}], "CATALOG_MEMBERSHIP")

    expected_columns = [(table, name, position, info) for table, cols in contract["tables"].items() for position, (name, info) in enumerate(cols.items(), 1)]
    require(len(groups["column"]) == len(expected_columns), "CATALOG_COLUMN_COUNT")
    columns = {(r["table"], r["name"]): r for r in groups["column"]}
    require(len(columns) == len(expected_columns), "CATALOG_DUPLICATE_COLUMN")
    for table, name, position, expected in expected_columns:
        r = columns.get((table, name))
        require(r is not None, "CATALOG_COLUMN_MISSING")
        typ = {"timestamptz":"timestamp with time zone", "epoch_ms":SCHEMA + ".epoch_ms"}.get(expected["type"], expected["type"])
        require(r["position"] == position and r["type"] == typ and r["not_null"] is expected["not_null"] and r["identity"] == expected["identity"] and r["generated"] == expected["generated"] and r["dropped"] is False, "CATALOG_COLUMN_CONTRACT")
        require((r["default"] is not None) == bool(expected["default_source"]) and (r["default"] is None or _bounded_definition(r["default"])), "CATALOG_COLUMN_DEFAULT")
        require(r["collation"] == ("default" if expected["type"] in ("text", "text[]") else None), "CATALOG_COLUMN_COLLATION")

    require(len(groups["function"]) == len(contract["functions"]) and {r["name"] for r in groups["function"]} == set(contract["functions"]), "CATALOG_FUNCTION_INVENTORY")
    for r in groups["function"]:
        e = contract["functions"][r["name"]]
        require(_oid(r["oid"]) and r["body"] == e["body"] and r["owner"] == e["owner"] and r["arguments"] == ", ".join(e["args"]) and (r["argument_names"] or []) == e["argnames"] and r["result"] == e["result"] and r["security_definer"] is e["security_definer"] and (r["config"] or []) == e["config"], "CATALOG_FUNCTION_SOURCE")
        require(r["argument_modes"] is None and r["argument_defaults"] == 0 and r["language"] == "plpgsql" and r["volatility"] == "v" and r["parallel"] == "u" and r["strict"] is False and r["leakproof"] is False and r["function_kind"] == "f" and r["set_returning"] is False and r["support"] == 0 and r["variadic"] == 0 and r["cost"] == 100 and r["rows"] == 0 and r["binary"] is None, "CATALOG_FUNCTION_ATTRIBUTES")
        require(_bounded_definition(r["definition"]) and ("AS $function$" + e["body"] + "$function$") in r["definition"], "CATALOG_FUNCTION_DEFINITION")
    require(len(groups["trigger"]) == len(contract["triggers"]), "CATALOG_TRIGGER_COUNT")
    expected_triggers = {(x["table"],x["name"],x["function"],x["kind"]) for x in contract["triggers"]}
    actual_triggers = {(x["table"],x["name"],x["function"],x["trigger_kind"]) for x in groups["trigger"]}
    require(expected_triggers == actual_triggers, "CATALOG_TRIGGER_INVENTORY")
    for r in groups["trigger"]:
        require(r["function_schema"] == SCHEMA and r["enabled"] == "O" and r["arguments"] == 0 and r["argument_bytes"] == "" and r["qualification"] is None and r["columns"] == "" and r["constraint"] == 0 and r["deferrable"] is False and r["deferred"] is False and r["old_table"] is None and r["new_table"] is None, "CATALOG_TRIGGER_ATTRIBUTES")

    # PostgreSQL 18 catalogs NOT NULL as contype='n'. These are source-derived
    # alongside explicit CHECK/PK/UNIQUE/FK contracts, not learned from readback.
    expected = expected_constraints(contract)
    require(Counter(_constraint_key(c) for c in expected) == Counter(_constraint_key(c, True) for c in groups["constraint"]), "CATALOG_CONSTRAINT_CONTRACT")
    named = {(c["table"],c["catalog_name"]):_constraint_key(c) for c in expected}
    actual_named = {(c["table"],c["name"]):_constraint_key(c,True) for c in groups["constraint"]}
    require(len(actual_named) == len(groups["constraint"]) and all(actual_named.get(k) == v for k,v in named.items()), "CATALOG_CONSTRAINT_IDENTITY")
    for c in groups["constraint"]:
        require(type(c["name"]) is str and re.fullmatch(r"[a-z_][a-z0-9_]{0,62}", c["name"]), "CATALOG_CONSTRAINT_NAME")
        require(c["validated"] is True and c["enforced"] is True and c["local"] is True and c["inherit_count"] == 0 and _bounded_definition(c["definition"]), "CATALOG_CONSTRAINT_ATTRIBUTES")
        # NOT NULL is not inheritable in the same way as table CHECK; PG18 may
        # set connoinherit true. CHECK must retain the source default false.
        require(c["constraint_kind"] == 'n' or c["no_inherit"] is False, "CATALOG_CONSTRAINT_INHERITANCE")
        if c["constraint_kind"] == "f":
            require(c["ref_schema"] == SCHEMA and c["match"] == "s" and c["update_action"] == "a" and c["delete_action"] == "a", "CATALOG_FK_ACTION")
        else:
            require(c["ref_schema"] is None and c["match"] == " " and c["update_action"] == " " and c["delete_action"] == " ", "CATALOG_NON_FK_ATTRIBUTES")

    expected_internal = []
    for c in groups["constraint"]:
        if c["constraint_kind"] != "f":
            continue
        for table, function, trigger_kind in (
            (c["table"], "RI_FKey_check_ins", 5),
            (c["table"], "RI_FKey_check_upd", 17),
            (c["ref_table"], "RI_FKey_noaction_del", 9),
            (c["ref_table"], "RI_FKey_noaction_upd", 17),
        ):
            expected_internal.append({"kind":"internal_trigger", "table":table,
                "constraint_table":c["table"], "constraint_name":c["name"],
                "function":function,"function_schema":"pg_catalog","trigger_kind":trigger_kind,
                "enabled":"O","deferrable":c["deferrable"],"deferred":c["deferred"],
                "arguments":0,"argument_bytes":"","qualification":None,"columns":""})
    require(Counter(canonical(x) for x in groups["internal_trigger"]) == Counter(canonical(x) for x in expected_internal), "CATALOG_INTERNAL_TRIGGER_CONTRACT")

    expected_indexes = {x["name"]:x for x in contract["indexes"]}
    for c in groups["constraint"]:
        if c["constraint_kind"] in ("p", "u"):
            require(type(c["index"]) is str and re.fullmatch(r"[a-z_][a-z0-9_]{0,62}",c["index"]) and c["index"] not in expected_indexes, "CATALOG_CONSTRAINT_INDEX")
            require(c["index"] == c["name"], "CATALOG_CONSTRAINT_INDEX_NAME")
            expected_indexes[c["index"]] = {"table":c["table"],"keys":tuple(c["keys"]),"options":(0,)*len(c["keys"]),"unique":True,"primary":c["constraint_kind"]=="p","predicate_source":""}
    require(len(groups["index"]) == len(expected_indexes) and {r["name"] for r in groups["index"]} == set(expected_indexes), "CATALOG_INDEX_INVENTORY")
    for r in groups["index"]:
        e = expected_indexes[r["name"]]
        require(r["table"] == e["table"] and tuple(r["keys"]) == e["keys"] and r["options"] == " ".join(str(n) for n in e["options"]) and r["unique"] is e["unique"] and r["primary"] is e.get("primary",False) and all(r[x] is True for x in ("valid","ready","live")) and r["nulls_not_distinct"] is False and r["key_count"] == len(e["keys"]) and r["attribute_count"] == len(e["keys"]) and r["method"] == "btree" and r["expressions"] is None and (r["predicate"] is not None) == bool(e["predicate_source"]) and _bounded_definition(r["definition"]), "CATALOG_INDEX_CONTRACT")
    expected_relations = {**{t:"r" for t in contract["tables"]}, **{i:"i" for i in expected_indexes}, "outbox_id_seq":"S"}
    require(len(groups["relation"]) == len(expected_relations) and {r["name"] for r in groups["relation"]} == set(expected_relations), "CATALOG_RELATION_INVENTORY")
    for r in groups["relation"]:
        require(r["relation_kind"] == expected_relations[r["name"]] and r["owner"] == OWNER and r["persistence"] == "p" and r["rls"] is False and r["force_rls"] is False and r["partition"] is False and r["options"] is None, "CATALOG_RELATION_ATTRIBUTES")
    expected_types = {"epoch_ms":("d",None,None,"int8"), "_epoch_ms":("b",None,"epoch_ms",None)}
    for table in contract["tables"]:
        expected_types[table] = ("c",table,None,None)
        expected_types["_"+table] = ("b",None,table,None)
    require(len(groups["type"]) == len(expected_types) and {r["name"] for r in groups["type"]} == set(expected_types), "CATALOG_TYPE_INVENTORY")
    for r in groups["type"]:
        require((r["type_kind"],r["relation"],r["element"],r["base_type"]) == expected_types[r["name"]] and r["owner"] == OWNER and r["not_null"] is False and r["default"] is None, "CATALOG_TYPE_CONTRACT")
    require(groups["sequence"] == [{"kind":"sequence","name":"outbox_id_seq","data_type":"bigint","start":1,"increment":1,"min":1,"max":9223372036854775807,"cache":1,"cycle":False,"table":"outbox","column":"id","dependency":"i"}], "CATALOG_SEQUENCE_CONTRACT")

    actual_acls = []
    owner_privileges = {"table":set("INSERT SELECT UPDATE DELETE TRUNCATE REFERENCES TRIGGER MAINTAIN".split()),"sequence":set("USAGE SELECT UPDATE".split()),"function":{"EXECUTE"},"schema":{"USAGE","CREATE"},"database":{"CONNECT","CREATE","TEMPORARY"},"column":set()}
    owner_acls = Counter()
    for r in groups["acl"]:
        require(r["scope"] in owner_privileges and r["grantable"] is False and r["grantor"] == r["owner"] and r["owner"] == (ROUTINE if r["scope"] == "function" and r["object"] == CONSUME else OWNER), "CATALOG_ACL_GRANTOR")
        if r["grantee"] == r["owner"]:
            require(r["privilege"] in owner_privileges[r["scope"]], "CATALOG_OWNER_ACL")
            owner_acls[(r["scope"],r["object"],r["privilege"])] += 1
        else:
            actual_acls.append((r["scope"],r["object"],r["column"],r["grantee"],r["privilege"]))
    require(sorted(actual_acls) == contract["acls"], "CATALOG_ACL_EXACT_GRANTS")
    expected_owner_acls = Counter()
    for scope, objects in (("table",contract["tables"]), ("sequence",["outbox_id_seq"]), ("function",contract["functions"]), ("schema",[SCHEMA]), ("database",[""])):
        for name in objects:
            for privilege in owner_privileges[scope]:
                expected_owner_acls[(scope,name,privilege)] += 1
    require(owner_acls == expected_owner_acls, "CATALOG_OWNER_ACL_INVENTORY")
    require(len(groups["row_count"]) == len(contract["tables"]) and {r["table"] for r in groups["row_count"]} == set(contract["tables"]), "CATALOG_ROW_COUNT_INVENTORY")
    for r in groups["row_count"]:
        require(type(r["count"]) is int and r["count"] == (1 if r["table"] in ("tenants","users","developer_apps","scope_limits") else 0), "CATALOG_NON_PRISTINE_DATA")
    require(groups["seed"] == [{"kind":"seed","exact":True}], "CATALOG_SYNTHETIC_SEED")
    routine = next(x for x in groups["function"] if x["name"] == CONSUME)
    # Full bounded actual definitions are PRIVATE. The public digest is only a
    # transcript/content binding, never a claim that its bytes were predicted.
    private = {"format":"worktool-registration-install-receipt-v1", "proof_model":"trusted-exact-source-installation-and-source-derived-structural-contracts", "constraint_semantic_normalization":"NOT_PROVEN", "sql_sources":dict(SOURCE_PINS), "sql_manifest_sha256":bundle.sql_manifest_sha256, "execution":execution_receipt, "expected_contract":contract, "observed_catalog":raw}
    catalog_digest = sha(canonical(raw))
    receipt_digest = sha(canonical(private))
    metadata = {"database":database, "database_oid":identity["database_oid"], "schema_oid":identity["schema_oid"], "routine_oid":routine["oid"], "routine_definition_sha256":sha(routine["definition"].encode()), "boundary_sha256":SOURCE_PINS[BOUNDARY_PATH], "registry_sha256":SOURCE_PINS[REGISTRY_PATH], "sql_manifest_sha256":bundle.sql_manifest_sha256, "catalog_observation_sha256":catalog_digest, "install_receipt_sha256":receipt_digest}
    metadata.update({key:role_oids[role] for key,role in (("owner_oid",OWNER),("issuer_oid",ISSUER),("consumer_oid",CONSUMER),("routine_owner_oid",ROUTINE),("unrelated_oid",UNRELATED))})
    public = {"format":"worktool-registration-install-summary-v1", "status":"VERIFIED_TRUSTED_INSTALLATION_PROVENANCE", "sql_manifest_sha256":bundle.sql_manifest_sha256, "catalog_observation_sha256":catalog_digest, "install_receipt_sha256":receipt_digest, "table_count":len(contract["tables"]), "function_count":len(contract["functions"]), "trigger_count":len(contract["triggers"]), "constraint_count":len(expected), "constraint_semantic_normalization":"NOT_PROVEN", "acceptance_tests":"NOT_RUN", "sql_sources":dict(SOURCE_PINS), "execution":execution_receipt}
    return InstallReceipt(metadata, public, private)


def preflight_field_diagnostics(result, expected):
    """Fixed categories only, derived from the already-returned first result.

    This is deliberately NOT an acceptance predicate. The existing result ==
    expected comparison below remains the sole pristine-value decision. No SQL,
    observed values, names, counts, unexpected keys or exception text escapes.
    """
    keys = ("version", "session_user", "current_user", "database_absent",
            "roles_absent", "other_clients", "bootstrap_superuser", "event_triggers")
    checks = {key: "INVALID_SHAPE" for key in keys}
    shape = {key: "INVALID_SHAPE" for key in ("root", "row_count", "row", "fields")}
    if type(result) is list:
        shape["root"] = "MATCH"
        if len(result) == 1:
            shape["row_count"] = "MATCH"
            if type(result[0]) is dict:
                shape["row"] = "MATCH"
                row = result[0]
                if set(row) == set(keys):
                    shape["fields"] = "MATCH"
                for key in keys:
                    if key in row and type(row[key]) is type(expected[0][key]):
                        checks[key] = "MATCH" if row[key] == expected[0][key] else "MISMATCH"
    if "INVALID_SHAPE" in shape.values() or "INVALID_SHAPE" in checks.values():
        classification = "INVALID_SHAPE"
    elif "MISMATCH" in checks.values():
        classification = "VALUE_MISMATCH"
    else:
        classification = "VALUE_MATCH"
    return {"format": "worktool-registration-preflight-fields-v1",
            "authority": "DIAGNOSTIC_ONLY", "classification": classification,
            "shape": shape, "checks": checks}


def _record_preflight_diagnostics(executor, result, expected):
    # An absent/unavailable diagnostic sink must never change the old decision,
    # add a call to PostgreSQL, retry an action, or expose an exception message.
    try:
        recorder = getattr(executor, "record_preflight_diagnostics", None)
        if callable(recorder):
            recorder(preflight_field_diagnostics(result, expected))
    except Exception:
        pass


def provision(executor, bundle, database, bootstrap_role, role_passwords):
    """One attempt, no retries/fallback or autonomous process/role discovery.

    Resource/image/new-cluster verification and exact cleanup ownership belong
    to the supervisor, before calling this function. No success is emitted on
    absent/failed acknowledgement, malformed readback, or unknown outcome.
    """
    plan = build_provision_plan(bundle, database, bootstrap_role, role_passwords)
    receipts = []
    catalog = None
    for step in plan:
        try:
            result = executor.execute(step)
        except Exception:
            raise ProvisionError("PROVISION_EXECUTION_FAILED_STOP_NO_RETRY") from None
        if step.phase == "bootstrap_preflight":
            expected = [{"version":VERSION,"session_user":bootstrap_role,"current_user":bootstrap_role,"database_absent":True,"roles_absent":True,"other_clients":0,"bootstrap_superuser":True,"event_triggers":0}]
            _record_preflight_diagnostics(executor, result, expected)
            require(result == expected, "BOOTSTRAP_NOT_PRISTINE")
        elif step.query:
            catalog = result
        else:
            require(result == [], "PROVISION_UNEXPECTED_OUTPUT_STOP_NO_RETRY")
        receipts.append({"phase":step.phase,"login_role":step.login_role,"database":step.database,"sql_sha256":sha(step.sql.encode()),"acknowledged":True})
    require(catalog is not None, "CATALOG_NOT_READ")
    return validate_catalog(catalog, bundle, database, receipts)


def _definition_vocabulary(texts):
    source = "\n".join(text for _,text in texts)
    words = set(re.findall(r"[A-Za-z_][A-Za-z0-9_]*", source))
    words |= set("CREATE UNIQUE INDEX USING btree ON CHECK PRIMARY KEY FOREIGN REFERENCES NOT NULL DEFERRABLE INITIALLY DEFERRED DEFAULT GENERATED ALWAYS AS STORED ANY ALL ARRAY COLLATE NO INHERIT bigint integer numeric boolean text bytea oid xid8 json jsonb timestamp with time zone double precision character varying pg_catalog wt_synthetic_registry true false".split())
    words = {x.lower() for x in words}
    numerals = set(re.findall(r"\b[0-9]+(?:\.[0-9]+)?\b", source))
    literals = set()
    def collect(raw):
        for kind,part in _pieces(raw):
            if kind == "literal":
                literals.add(part[1:-1].replace("''", "'"))
            elif kind == "dollar":
                delim = re.match(r"\$[A-Za-z_0-9]*\$",part)[0]
                collect(part[len(delim):-len(delim)])
    collect(source)
    return words, numerals, literals


def _safe_definition_text(value, texts, vocabulary=None):
    """Bounded lexical redaction gate, NOT an expression-equivalence proof.

    Catalog deparser output may add casts, parentheses, ARRAY/ANY rewrites and
    type spellings. It may not introduce a new literal, identifier or comment.
    This guards artifact content independently of the trusted-install model.
    """
    require(_bounded_definition(value), "CATALOG_ARTIFACT_DEFINITION_BOUNDS")
    words, numerals, literals = vocabulary if vocabulary is not None else _definition_vocabulary(texts)
    plain = []
    for kind,part in _pieces(value):
        require(kind not in ("comment","dollar"), "CATALOG_ARTIFACT_UNREVIEWED_SYNTAX")
        if kind == "literal":
            literal = part[1:-1].replace("''", "'")
            require(literal in literals or literal in numerals, "CATALOG_ARTIFACT_UNREVIEWED_LITERAL")
            plain.append(" ")
        elif kind == "identifier":
            require(part[1:-1].replace('""','"').lower() in words, "CATALOG_ARTIFACT_UNREVIEWED_IDENTIFIER")
            plain.append(" ")
        else:
            plain.append(part)
    rest = "".join(plain)
    require(all(x.lower() in words for x in re.findall(r"[A-Za-z_][A-Za-z0-9_]*",rest)), "CATALOG_ARTIFACT_UNREVIEWED_IDENTIFIER")
    require(all(x in numerals for x in re.findall(r"\b[0-9]+(?:\.[0-9]+)?\b",rest)), "CATALOG_ARTIFACT_UNREVIEWED_NUMBER")
    require(re.sub(r"[A-Za-z_][A-Za-z0-9_]*|[0-9]+(?:\.[0-9]+)?|[\s.,()\[\]=~!<>+*/%&|:#@?^-]", "", rest) == "", "CATALOG_ARTIFACT_UNREVIEWED_SYNTAX")
    return value


def sanitized_catalog_proof(receipt):
    """Produce the sole upload-safe catalog artifact; reject, never echo extras.

    Exact validated function prosrc is source text, not row data. Function
    deparser headers are omitted (only their digest is retained). Constraint,
    index/default expressions additionally pass a source-literal/token gate.
    This lexical gate prevents unreviewed content from being uploaded; it does
    not upgrade the explicit NOT_PROVEN semantic-normalization limitation.
    """
    require(type(receipt) is InstallReceipt, "CATALOG_ARTIFACT_RECEIPT_TYPE")
    private = receipt.private_catalog_receipt
    require(set(private) == {"format","proof_model","constraint_semantic_normalization","sql_sources","sql_manifest_sha256","execution","expected_contract","observed_catalog"}, "CATALOG_ARTIFACT_RECEIPT_FIELDS")
    require(private["sql_sources"] == SOURCE_PINS, "CATALOG_ARTIFACT_SOURCE_PINS")
    # The received receipt must still be exactly the object that was validated.
    require(sha(canonical(private)) == receipt.private_metadata.get("install_receipt_sha256") and receipt.public_evidence.get("install_receipt_sha256") == receipt.private_metadata.get("install_receipt_sha256"), "CATALOG_ARTIFACT_RECEIPT_CHANGED")
    # Build the redaction vocabulary only from hash-bound source contracts.
    contract = private["expected_contract"]
    chunks = []
    chunks += [c["source"] for c in contract["constraints"]]
    chunks += ["CREATE FUNCTION " + name + " " + fn["body"] for name,fn in contract["functions"].items()]
    chunks += [" ".join((name,table,info["type"],info["default_source"])) for table,cols in contract["tables"].items() for name,info in cols.items()]
    chunks += [x["name"]+" "+x["table"]+" "+" ".join(x["keys"])+" "+x["predicate_source"] for x in contract["indexes"]]
    chunks += ["CHECK(VALUE>=0 AND VALUE<=9007199254740991)"]
    chunks += [c["catalog_name"] for c in expected_constraints(contract)]
    texts = (("verified-derived-contract", "\n".join(chunks)),)
    vocabulary = _definition_vocabulary(texts)
    groups = _bounded_rows(private["observed_catalog"])
    safe = []
    # Row counts/seed booleans are not data rows, but omit both entirely to keep
    # this deliverable schema-only. Identity includes only validated target IDs.
    allowed = set(FIELDS) - {"row_count","seed"}
    for kind in sorted(allowed):
        for row in groups[kind]:
            item = dict(row)
            if kind == "function":
                expected = contract["functions"].get(row["name"])
                require(expected is not None and row["body"] == expected["body"], "CATALOG_ARTIFACT_FUNCTION_BODY")
                item["definition_sha256"] = sha(item.pop("definition").encode())
            if kind in ("constraint", "index"):
                item["definition"] = _safe_definition_text(item["definition"], texts, vocabulary)
            for field_name in ("default", "predicate", "expressions"):
                if kind in ("column", "index") and item.get(field_name) is not None:
                    item[field_name] = _safe_definition_text(item[field_name], texts, vocabulary)
            safe.append(item)
    result = {"format":"worktool-registration-catalog-proof-v1", "proof_model":private["proof_model"], "constraint_semantic_normalization":"NOT_PROVEN", "sql_sources":dict(SOURCE_PINS), "sql_manifest_sha256":private["sql_manifest_sha256"], "install_receipt_sha256":receipt.private_metadata["install_receipt_sha256"], "catalog_observation_sha256":receipt.private_metadata["catalog_observation_sha256"], "execution":private["execution"], "objects":safe}
    require(len(canonical(result)) <= MAX_RESULT_BYTES, "CATALOG_ARTIFACT_TOO_LARGE")
    return result
