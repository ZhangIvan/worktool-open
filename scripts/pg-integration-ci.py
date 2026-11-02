#!/usr/bin/env python3
"""Candidate CI resource harness. No resources are touched by --selftest."""
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import subprocess
import sys
import time
from urllib.parse import urlparse

IMAGE = 'postgres@sha256:41a369edc91d9bdd7a58be6bc71ea0f8f7a3f49e82818a9f0829021ea7840b7c'
ALLOW = 'isolated-synthetic-schema-only'
LABEL = 'io.worktool.pg-ci-run'
SAFE_CODES = frozenset({'EXTRA_PORT', 'VERSION_STRING_MISMATCH', 'STORE_PRIVILEGED', 'GO_TEST_FAILED', 'RESOURCE_LIMIT_MISMATCH', 'PERSISTENT_OR_FOREIGN_MOUNT', 'SUBPROCESS_NONZERO', 'PG_READY_TIMEOUT', 'RUN_DIRECTORY_ALREADY_EXISTS', 'NON_LOOPBACK_PORT', 'STORE_INHERITS_OWNER', 'UNSAFE_SECRET_DIRECTORY', 'CONNECTION_LIMIT_MISMATCH', 'NON_JSON_TEST_OUTPUT', 'SUBPROCESS_FAILED_OR_TIMEOUT', 'DATABASE_TIMEOUT_MISMATCH', 'NAMESPACE_MISMATCH', 'DATA_DIRECTORY_MISMATCH', 'INVALID_RUN_ID', 'MISSING_FAILED_OR_SKIPPED_INTEGRATION_CASE', 'TMPFS_MISMATCH', 'MISSING_ALLOW', 'CLEANUP_NOT_CONFIRMED', 'CLEANUP_IDENTITY_MISMATCH', 'INVALID_PORT', 'HOSTED_CI_ONLY', 'VERSION_MISMATCH', 'IDENTITY_MISMATCH', 'INVALID_TEST_DSN', 'WAL_OUTSIDE_TMPFS', 'ROLE_OR_TARGET_MISMATCH'})
TESTS = {'TestPGIntegrationSchemaAndDeviceBindings', 'TestPGIntegrationReservationBindings', 'TestPGIntegrationValidMaterializationAndGC', 'TestPGIntegrationInitialInsertForeignKeys'} | {f'TestPGAdmissionATC{i:02d}' for i in range(1, 9)} | {'TestPGAdmissionPayloadStorage', 'TestPGAdmissionPayloadMigrationGuard'}


def require(ok, code):
    if not ok:
        raise RuntimeError(code)


def invoke(args, *, data=None, env=None, timeout=60, check=True):
    # Never echo argv, stdin, output or exception details: these may contain SQL.
    try:
        p = subprocess.run(args, input=data, stdout=subprocess.PIPE,
                           stderr=subprocess.PIPE, env=env, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        raise RuntimeError('SUBPROCESS_FAILED_OR_TIMEOUT') from None
    if check:
        require(p.returncode == 0, 'SUBPROCESS_NONZERO')
    return p


def identity():
    require(os.environ.get('GITHUB_ACTIONS') == 'true' and
            os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted' and
            os.environ.get('RUNNER_OS') == 'Linux', 'HOSTED_CI_ONLY')
    run, attempt = os.environ.get('GITHUB_RUN_ID', ''), os.environ.get('GITHUB_RUN_ATTEMPT', '')
    require(re.fullmatch(r'[0-9]{1,20}', run) and re.fullmatch(r'[0-9]{1,5}', attempt), 'INVALID_RUN_ID')
    token = run + '-' + attempt
    root = Path(os.environ['RUNNER_TEMP']).resolve()
    return 'wt-pg-' + token, token, root / ('wt-pg-' + token)


def private_file(path, data):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as f:
        f.write(data)


def inspect(name):
    # Go-template whitelist: do not materialize Config.Env / full inspect.
    template = '{"name":{{json .Name}},"label":{{json (index .Config.Labels "' + LABEL + '")}},"mounts":{{json .Mounts}},"tmpfs":{{json .HostConfig.Tmpfs}},"memory":{{.HostConfig.Memory}},"swap":{{.HostConfig.MemorySwap}},"cpus":{{.HostConfig.NanoCpus}},"privileged":{{.HostConfig.Privileged}},"network":{{json .HostConfig.NetworkMode}},"pid":{{json .HostConfig.PidMode}},"ports":{{json .NetworkSettings.Ports}}}'
    return json.loads(invoke(['docker', 'inspect', '--format', template, name]).stdout)


def verify_inspect(v, name, token):
    require(v['name'] == '/' + name and v['label'] == token, 'IDENTITY_MISMATCH')
    require(v['memory'] == 1073741824 and v['swap'] == 1073741824 and v['cpus'] == 2000000000, 'RESOURCE_LIMIT_MISMATCH')
    require(v['privileged'] is False and v['network'] == 'bridge' and v['pid'] == '', 'NAMESPACE_MISMATCH')
    require(v['tmpfs'] == {'/var/lib/postgresql': 'rw,nosuid,nodev,size=2147483648'}, 'TMPFS_MISMATCH')
    # Docker may expose --tmpfs only in HostConfig.Tmpfs, or also in Mounts.
    require(all(m['Type'] == 'tmpfs' and m['Destination'] == '/var/lib/postgresql' for m in v['mounts']), 'PERSISTENT_OR_FOREIGN_MOUNT')
    require(set(v['ports']) == {'5432/tcp'}, 'EXTRA_PORT')
    ports = v['ports']['5432/tcp']
    require(len(ports) == 1 and ports[0]['HostIp'] == '127.0.0.1', 'NON_LOOPBACK_PORT')
    port = ports[0]['HostPort']
    require(re.fullmatch('[0-9]{1,5}', port) and 1 <= int(port) <= 65535, 'INVALID_PORT')
    return port


def preflight_env(env):
    require(env.get('WORKTOOL_PGTEST_ALLOW') == ALLOW, 'MISSING_ALLOW')
    vals = [urlparse(env.get(k, '')) for k in ('WORKTOOL_PGTEST_OWNER_DSN', 'WORKTOOL_PGTEST_STORE_DSN')]
    a, b = vals
    for v in vals:
        require(v.scheme == 'postgresql' and v.hostname == '127.0.0.1' and v.port and
                re.fullmatch(r'/wt_test_[a-f0-9]+', v.path) and v.username and v.password and
                v.query == 'sslmode=disable' and not v.fragment, 'INVALID_TEST_DSN')
    require((a.hostname, a.port, a.path) == (b.hostname, b.port, b.path) and a.username != b.username, 'ROLE_OR_TARGET_MISMATCH')


def result_events(raw):
    passed, failed, skipped = set(), set(), set()
    for line in raw.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            raise RuntimeError('NON_JSON_TEST_OUTPUT') from None
        name, action = event.get('Test'), event.get('Action')
        if action == 'skip':
            skipped.add(name or 'package')
        if action == 'fail':
            failed.add(name or 'package')
        if name in TESTS and action == 'pass':
            passed.add(name)
    require(not skipped and not failed and passed == TESTS, 'MISSING_FAILED_OR_SKIPPED_INTEGRATION_CASE')
    return [{'test': n, 'status': 'PASS'} for n in sorted(passed)]



def test_diagnostics(raw):
    # Only fixed test IDs / enumerated SQLSTATE tokens, NEVER arbitrary Output.
    failed, states = set(), set()
    approved_states = {'23503', '23505', '23514', '40001', '40P01', '57014', '55P03'}
    for line in raw.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if not isinstance(event, dict):
            continue
        if event.get('Action') == 'fail' and event.get('Test') in TESTS:
            failed.add(event['Test'])
        output = event.get('Output', '')
        if isinstance(output, str):
            states.update(x for x in re.findall(r'\bSQLSTATE=([A-Z0-9]{5})\b', output) if x in approved_states)
    return {'failedTests': sorted(failed), 'sqlstates': sorted(states)}

def cleanup():
    name, token, folder = identity()
    # Filter returns no output for absent container; daemon errors remain failures.
    ids = invoke(['docker', 'ps', '-aq', '--filter', 'name=^/' + name + '$']).stdout.strip()
    if ids:
        v = inspect(name)
        require(v['name'] == '/' + name and v['label'] == token, 'CLEANUP_IDENTITY_MISMATCH')
        invoke(['docker', 'rm', '-f', name])
    require(not invoke(['docker', 'ps', '-aq', '--filter', 'name=^/' + name + '$']).stdout.strip(), 'CLEANUP_NOT_CONFIRMED')
    if folder.exists():
        require(not folder.is_symlink(), 'UNSAFE_SECRET_DIRECTORY')
        shutil.rmtree(folder)
    print('PG_CLEANUP_CONFIRMED')


def ready_probe(name, admin, db):
    # The image's temporary init server is Unix-socket-only. Probe final TCP.
    return invoke(['docker', 'exec', name, 'pg_isready', '-q',
                   '-h', '127.0.0.1', '-p', '5432', '-U', admin, '-d', db], check=False)


def run():
    name, token, folder = identity()
    require(not folder.exists(), 'RUN_DIRECTORY_ALREADY_EXISTS')
    folder.mkdir(mode=0o700)
    # Randomized bootstrap, migration-owner and nonowner-store identities.
    suffix = secrets.token_hex(8)
    admin, owner, store = ['wt_' + role + '_' + suffix for role in ('admin', 'owner', 'store')]
    admin_pw, owner_pw, store_pw = [secrets.token_hex(32) for _ in range(3)]
    db = 'wt_test_' + suffix
    envfile = folder / 'container.env'
    private_file(envfile, f'POSTGRES_USER={admin}\nPOSTGRES_PASSWORD={admin_pw}\nPOSTGRES_DB={db}\nPOSTGRES_INITDB_ARGS=--auth-host=scram-sha-256\nPGDATA=/var/lib/postgresql/18/docker\n')
    try:
        invoke(['docker', 'pull', '--platform', 'linux/amd64', IMAGE], timeout=180)
        invoke(['docker', 'run', '-d', '--name', name, '--label', LABEL + '=' + token,
                '--platform', 'linux/amd64', '--network', 'bridge', '--cpus', '2',
                '--memory', '1g', '--memory-swap', '1g', '--shm-size', '64m',
                '--tmpfs', '/var/lib/postgresql:rw,nosuid,nodev,size=2147483648',
                '--publish', '127.0.0.1::5432', '--env-file', str(envfile),
                IMAGE, 'postgres', '-c', 'max_connections=10',
                '-c', 'log_statement=none', '-c', 'log_min_error_statement=panic',
                '-c', 'statement_timeout=5s', '-c', 'lock_timeout=1s',
                '-c', 'idle_in_transaction_session_timeout=5s'])
        port = verify_inspect(inspect(name), name, token)
        def sql(query):
            return invoke(['docker', 'exec', '-i', name, 'psql', '-X', '-q', '-A', '-t',
                           '-v', 'ON_ERROR_STOP=1', '-U', admin, '-d', db], data=query.encode()).stdout.decode().strip()
        ready = False
        for _ in range(45):
            p = ready_probe(name, admin, db)
            if p.returncode == 0:
                ready = True
                break
            time.sleep(1)
        require(ready, 'PG_READY_TIMEOUT')
        require(sql('SHOW server_version_num;') == '180006', 'VERSION_MISMATCH')
        version = sql('SELECT version();')
        require(version.startswith('PostgreSQL 18.6 '), 'VERSION_STRING_MISMATCH')
        require(sql('SHOW max_connections;') == '10', 'CONNECTION_LIMIT_MISMATCH')
        for setting, expected in [('statement_timeout', '5s'), ('lock_timeout', '1s'), ('idle_in_transaction_session_timeout', '5s')]:
            require(sql('SHOW ' + setting + ';') == expected, 'DATABASE_TIMEOUT_MISMATCH')
        require(sql('SHOW data_directory;') == '/var/lib/postgresql/18/docker', 'DATA_DIRECTORY_MISMATCH')
        wal = invoke(['docker', 'exec', name, 'readlink', '-f', '/var/lib/postgresql/18/docker/pg_wal']).stdout.decode().strip()
        require(wal == '/var/lib/postgresql/18/docker/pg_wal', 'WAL_OUTSIDE_TMPFS')
        # Values are CSPRNG lowercase hex / generated ASCII identifiers, never input.
        sql(f"CREATE ROLE {owner} LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD '{owner_pw}';\nCREATE ROLE {store} LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS PASSWORD '{store_pw}';\nALTER DATABASE {db} OWNER TO {owner};\nREVOKE ALL ON DATABASE {db} FROM PUBLIC;\nGRANT CONNECT ON DATABASE {db} TO {store};\n")
        require(sql(f"SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolbypassrls FROM pg_roles WHERE rolname='{store}';") == 'f', 'STORE_PRIVILEGED')
        require(sql(f"SELECT pg_has_role('{store}','{owner}','MEMBER');") == 'f', 'STORE_INHERITS_OWNER')
        env = os.environ.copy()
        env.update(WORKTOOL_PGTEST_ALLOW=ALLOW,
                   WORKTOOL_PGTEST_OWNER_DSN=f'postgresql://{owner}:{owner_pw}@127.0.0.1:{port}/{db}?sslmode=disable',
                   WORKTOOL_PGTEST_STORE_DSN=f'postgresql://{store}:{store_pw}@127.0.0.1:{port}/{db}?sslmode=disable')
        preflight_env(env)
        private_file(folder / 'test.env', '\n'.join(k + '=' + env[k] for k in ('WORKTOOL_PGTEST_ALLOW', 'WORKTOOL_PGTEST_OWNER_DSN', 'WORKTOOL_PGTEST_STORE_DSN')) + '\n')
        p = invoke(['go', 'test', '-json', '-count=1', '-p', '1', '-timeout', '10m',
                    './internal/store/pg', '-run', '^TestPG(Integration|Admission(ATC|Payload))'], env=env, timeout=620, check=False)
        # Neither stdout Output records nor stderr are emitted or uploaded.
        diagnostics = test_diagnostics(p.stdout)
        if diagnostics['failedTests'] or diagnostics['sqlstates']:
            print(json.dumps({'testDiagnostics': diagnostics}))
        report = result_events(p.stdout)
        require(p.returncode == 0, 'GO_TEST_FAILED')
        checksums = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(Path('migrations').glob('*.sql'))}
        print(json.dumps({'image': IMAGE, 'version': '18.6', 'dataDirectory': '/var/lib/postgresql/18/docker', 'walDirectory': wal, 'memoryBytes': 1073741824, 'memorySwapBytes': 1073741824, 'cpus': 2, 'tmpfsBytes': 2147483648, 'hostIP': '127.0.0.1', 'port': int(port), 'mountPolicy': 'BOUNDED_TMPFS_VERIFIED', 'migrationSHA256': checksums, 'cases': report}))
    finally:
        cleanup()


def selftest():
    # Pure fixtures only: no subprocess, random credentials, Docker, Go or network.
    global invoke
    def forbidden(*args, **kwargs):
        raise AssertionError('selftest attempted subprocess')
    invoke = forbidden
    calls = []
    def fake_probe(args, **kwargs):
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args, 0, b'', b'')
    invoke = fake_probe
    assert ready_probe('wt-pg-1-1', 'wt_admin_fake', 'wt_test_abc').returncode == 0
    assert calls == [(['docker', 'exec', 'wt-pg-1-1', 'pg_isready', '-q', '-h', '127.0.0.1', '-p', '5432', '-U', 'wt_admin_fake', '-d', 'wt_test_abc'], {'check': False})]
    invoke = forbidden
    name, token = 'wt-pg-1-1', '1-1'
    v = dict(name='/' + name, label=token, mounts=[], tmpfs={'/var/lib/postgresql': 'rw,nosuid,nodev,size=2147483648'}, memory=1073741824, swap=1073741824, cpus=2000000000, privileged=False, network='bridge', pid='', ports={'5432/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '54321'}]})
    assert verify_inspect(v, name, token) == '54321'
    for key, bad in [('memory', 0), ('swap', -1), ('cpus', 0), ('privileged', True), ('network', 'host'), ('pid', 'host'), ('label', 'other'), ('mounts', [{'Type': 'volume', 'Destination': '/var/lib/postgresql'}]), ('tmpfs', {}), ('ports', {'5432/tcp': [{'HostIp': '0.0.0.0', 'HostPort': '54321'}]})]:
        try:
            verify_inspect(dict(v, **{key: bad}), name, token)
        except RuntimeError:
            continue
        raise AssertionError('unsafe inspect accepted')
    raw = b'\n'.join(json.dumps({'Test': n, 'Action': 'pass'}).encode() for n in TESTS)
    assert len(result_events(raw)) == 14
    old_four = b'\n'.join(json.dumps({'Test': n, 'Action': 'pass'}).encode() for n in TESTS if n.startswith('TestPGIntegration'))
    without_payload = b'\n'.join(json.dumps({'Test': n, 'Action': 'pass'}).encode() for n in TESTS if not n.startswith('TestPGAdmissionPayload'))
    for bad in [b'', old_four, without_payload, raw + b'\n{"Action":"skip"}', raw + b'\n{"Action":"fail"}']:
        try:
            result_events(bad)
        except RuntimeError:
            continue
        raise AssertionError('missing/skip/fail accepted')
    try:
        preflight_env({})
    except RuntimeError:
        pass
    else:
        raise AssertionError('missing gate accepted')
    env = {'WORKTOOL_PGTEST_ALLOW': ALLOW, 'WORKTOOL_PGTEST_OWNER_DSN': 'postgresql://owner:placeholder@127.0.0.1:54321/wt_test_abc?sslmode=disable', 'WORKTOOL_PGTEST_STORE_DSN': 'postgresql://store:placeholder@127.0.0.1:54321/wt_test_abc?sslmode=disable'}
    preflight_env(env)
    for bad in [env['WORKTOOL_PGTEST_OWNER_DSN'], env['WORKTOOL_PGTEST_STORE_DSN'].replace('127.0.0.1', 'example.test'), env['WORKTOOL_PGTEST_STORE_DSN'].replace('54321', '54322'), env['WORKTOOL_PGTEST_STORE_DSN'].replace('wt_test_abc', 'postgres'), env['WORKTOOL_PGTEST_STORE_DSN'] + '&host=example.test']:
        try:
            preflight_env(dict(env, WORKTOOL_PGTEST_STORE_DSN=bad))
        except RuntimeError:
            continue
        raise AssertionError('unsafe DSN accepted')
    diagnostics = test_diagnostics(b'{"Action":"fail","Test":"not_allowed_secret","Output":"postgresql://secret SQLSTATE=23503 SQLSTATE=SECRET"}')
    assert diagnostics == {'failedTests': [], 'sqlstates': ['23503']}
    assert 'secret' not in json.dumps(diagnostics)
    print('PASS pure selftest: resource/identity/mount/port gates, missing-env and missing/skip/fail result gates; PG NOT_RUN')


if __name__ == '__main__':
    os.umask(0o077)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    signal.signal(signal.SIGINT, lambda *_: sys.exit(130))
    try:
        {'run': run, 'cleanup': cleanup, '--selftest': selftest}[sys.argv[1]]()
    except (Exception, KeyboardInterrupt) as exc:
        # Never call str(exc): arbitrary subprocess/driver exceptions may leak.
        code = exc.args[0] if type(exc) is RuntimeError and len(exc.args) == 1 else None
        safe = code if isinstance(code, str) and code in SAFE_CODES else 'UNEXPECTED_REDACTED'
        print('PG_CI_FAILED:' + safe, file=sys.stderr)
        sys.exit(1)
