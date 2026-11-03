#!/usr/bin/env python3
"""Proposed hosted-CI-only browser slice. --selftest never starts a process."""
import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import socket
import stat
import subprocess
import sys
import time
from datetime import datetime, timedelta, timezone
from urllib.error import HTTPError
from urllib.request import Request, build_opener, ProxyHandler, HTTPRedirectHandler
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.dont_write_bytecode = True
GATES_SHA256 = '88c1ff0e5f63468e883ca96b4a7d35602110d6a21d21f9ea36c093841ee157bf'
if hashlib.sha256((ROOT / 'scripts/pg-integration-ci.py').read_bytes()).hexdigest() != GATES_SHA256:
    raise SystemExit('EXISTING_RESOURCE_GATES_CHANGED')
spec = importlib.util.spec_from_file_location('existing_pg_gates', ROOT / 'scripts/pg-integration-ci.py')
gates = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gates)
IMAGE = gates.IMAGE
ALLOW = 'isolated-synthetic-browser-only'
ORIGIN = 'http://127.0.0.1:5173'
DRIVER = 'http://127.0.0.1:9515'
# Fixed preinstalled locations used by the official hosted-runner Chromium and
# ChromeDriver installers. No PATH fallback, download, or permission repair.
CHROMIUM_BINARY = '/usr/local/share/chromium/chrome-linux/chrome'
CHROMEDRIVER_BINARY = '/usr/local/share/chromedriver-linux64/chromedriver'
PORTS = (3101, 5173, 9515)
MIGRATIONS = tuple(f'{i:04d}_{name}.sql' for i, name in enumerate((
    'base19', 'control7', 'admission2', 'admission_constraints', 'indexes_roles',
    'commands_payload_json', 'gateway_lease_connection', 'gateway_session_result'), 1))
CASES = frozenset({'startup', 'http-boundaries', 'browser-initial', 'user-revocation',
    'runtime-revocation', 'closed-gate', 'expired-gate', 'browser-restart', 'read-only-database', 'cleanup'})
PHASES = ('initial', 'revoked', 'gate-prepare', 'gate-closed', 'restart-prepare', 'restarted', 'cleanup')
STAGES = frozenset(PHASES) | {'preflight', 'pg-create', 'pg-seed', 'build', 'driver-start', 'app-start',
    'frontend-start', 'http-boundaries', 'runtime-revocation', 'expired-gate', 'app-restart', 'read-only-database', 'shutdown'}
stage = 'preflight'
DIAGNOSTIC_CHECKPOINTS = frozenset(('arguments selftest hosted-guard fixture-folder fixture-read fixture-schema profile-path '
    'chrome-binary profile-create state-create session-create session-save navigate signin-assert login-tenant login-name '
    'login-password login-submit bad-login-assert identity-assert devices-assert tenant-empty-assert owner-denied-assert '
    'logout-click logout-assert logout-refresh logout-persisted-assert state-read existing-url reload revoked-assert '
    'session-delete state-delete').split())
DIAGNOSTIC_CATEGORIES = frozenset(('validation filesystem transport timeout http webdriver malformed-response assertion unexpected-url unknown').split())
DIAGNOSTIC_ACTORS = frozenset(('none admin-bad admin operator owner other').split())
WEBDRIVER_ERRORS = frozenset(('element click intercepted|element not interactable|insecure certificate|invalid argument|'
    'invalid cookie domain|invalid element state|invalid selector|invalid session id|javascript error|move target out of bounds|'
    'no such alert|no such cookie|no such element|no such frame|no such shadow root|no such window|script timeout|'
    'session not created|stale element reference|timeout|unable to capture screen|unable to set cookie|unexpected alert open|'
    'unknown command|unknown error|unknown method|unsupported operation|detached shadow root').split('|'))


def browser_diagnostic(raw, phase):
    # This is a second independent whitelist before any child output reaches CI
    # logs. Never print driver messages, DOM, URLs, response bodies or credentials.
    try:
        need(len(raw) <= 4096, 'INVALID_DIAGNOSTIC')
        value = json.loads(raw)
        need(set(value) == {'phase', 'result', 'diagnostic'} and value['phase'] == phase
             and value['result'] == 'FAIL', 'INVALID_DIAGNOSTIC')
        d = value['diagnostic']
        need(isinstance(d, dict) and {'checkpoint', 'category', 'actor'} <= set(d)
             and set(d) <= {'checkpoint', 'category', 'actor', 'httpStatus', 'webdriverError'}, 'INVALID_DIAGNOSTIC')
        need(d['checkpoint'] in DIAGNOSTIC_CHECKPOINTS and d['category'] in DIAGNOSTIC_CATEGORIES
             and d['actor'] in DIAGNOSTIC_ACTORS, 'INVALID_DIAGNOSTIC')
        need('httpStatus' not in d or (type(d['httpStatus']) is int and 100 <= d['httpStatus'] <= 599), 'INVALID_DIAGNOSTIC')
        need('webdriverError' not in d or d['webdriverError'] in WEBDRIVER_ERRORS, 'INVALID_DIAGNOSTIC')
        return value
    except (ValueError, TypeError, KeyError, RuntimeError):
        return {'phase': phase, 'result': 'FAIL', 'diagnostic': {'checkpoint': 'arguments', 'category': 'unknown', 'actor': 'none'}}


def progress(value):
    global stage
    need(value in STAGES, 'INVALID_STAGE')
    stage = value


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def need(ok, code):
    if not ok:
        raise RuntimeError(code)


def identity(env=None):
    e = os.environ if env is None else env
    need(e.get('GITHUB_ACTIONS') == 'true' and e.get('RUNNER_ENVIRONMENT') == 'github-hosted'
         and e.get('RUNNER_OS') == 'Linux' and e.get('GITHUB_REPOSITORY') == 'ZhangIvan/worktool-open'
         and e.get('GITHUB_REF') == 'refs/heads/worktool/synthetic-prototype'
         and e.get('GITHUB_EVENT_NAME') == 'push' and e.get('WORKTOOL_CONSOLE_CI_ALLOW') == ALLOW,
         'HOSTED_CONSOLE_CI_ONLY')
    run, attempt = e.get('GITHUB_RUN_ID', ''), e.get('GITHUB_RUN_ATTEMPT', '')
    need(re.fullmatch(r'[0-9]{1,20}', run) and re.fullmatch(r'[0-9]{1,5}', attempt), 'INVALID_RUN_ID')
    root = Path(e.get('RUNNER_TEMP', ''))
    need(root.is_absolute() and root.resolve() == root, 'INVALID_RUN_ROOT')
    token = run + '-' + attempt
    return 'wt-console-' + token, token, root / ('wt-console-' + token)


def private(path, data, replace=False):
    raw = data if isinstance(data, bytes) else data.encode()
    target = path.with_name(path.name + '.new') if replace else path
    fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'wb') as f:
        f.write(raw)
    if replace:
        os.replace(target, path)


def record(folder, state):
    private(folder / 'state.json', json.dumps(state), replace=(folder / 'state.json').exists())


def invoke(args, data=None, timeout=60, env=None):
    return gates.invoke(args, data=data, timeout=timeout, env=env).stdout


def runtime_env():
    env = os.environ.copy()
    for key in ('HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'http_proxy', 'https_proxy', 'all_proxy'):
        env.pop(key, None)
    env['NODE_USE_ENV_PROXY'] = '0'
    return env


def proc_identity(pid):
    try:
        stat = Path(f'/proc/{pid}/stat').read_text().rsplit(') ', 1)[1].split()
        cmd = Path(f'/proc/{pid}/cmdline').read_bytes()
        return {'pid': pid, 'start': stat[19], 'group': int(stat[2]), 'cmdHash': hashlib.sha256(cmd).hexdigest()}
    except FileNotFoundError:
        return None


def group_members(group):
    found = []
    for path in Path('/proc').iterdir():
        if not path.name.isdigit():
            continue
        try:
            stat = (path / 'stat').read_text().rsplit(') ', 1)[1].split()
            if int(stat[2]) == group:
                found.append(int(path.name))
        except FileNotFoundError:
            pass
    return found


def stop_process(item, graceful=False):
    current = proc_identity(item['pid'])
    if current is None:
        need(not group_members(item['pid']), 'ORPHANED_PROCESS_GROUP')
        return
    need(current == item and item['group'] == item['pid'], 'PROCESS_IDENTITY_MISMATCH')
    os.killpg(item['pid'], signal.SIGTERM)
    exit_code = None
    deadline = time.monotonic() + 15
    while group_members(item['pid']) and time.monotonic() < deadline:
        # Reap only our own process if this is the original orchestrator.
        try:
            reaped, status = os.waitpid(item['pid'], os.WNOHANG)
            if reaped == item['pid']:
                exit_code = os.waitstatus_to_exitcode(status)
        except ChildProcessError:
            pass
        time.sleep(.1)
    if group_members(item['pid']):
        need(not graceful, 'GRACEFUL_SHUTDOWN_FAILED')
        os.killpg(item['pid'], signal.SIGKILL)
        time.sleep(.3)
    try:
        reaped, status = os.waitpid(item['pid'], os.WNOHANG)
        if reaped == item['pid']:
            exit_code = os.waitstatus_to_exitcode(status)
    except ChildProcessError:
        pass
    need(not group_members(item['pid']), 'PROCESS_CLEANUP_UNCONFIRMED')
    return exit_code


def port_free(port):
    with socket.socket() as s:
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            s.bind(('127.0.0.1', port))
            return True
        except OSError:
            return False


def remove_owned_container(name, token):
    need(re.fullmatch(r'[0-9]{1,20}-[0-9]{1,5}', token) and name == 'wt-console-' + token,
         'CLEANUP_IDENTITY_MISMATCH')
    ids = invoke(['docker', 'ps', '-aq', '--filter', 'name=^/' + name + '$']).strip()
    if ids:
        value = gates.inspect(name)
        # Resource verification may be the original failure. Removal requires
        # identity, not that the unsafe owned container first become safe.
        need(value['name'] == '/' + name and value['label'] == token, 'CLEANUP_IDENTITY_MISMATCH')
        invoke(['docker', 'rm', '-f', name])
    need(not invoke(['docker', 'ps', '-aq', '--filter', 'name=^/' + name + '$']).strip(), 'PG_CLEANUP_UNCONFIRMED')


def cleanup():
    name, token, folder = identity()
    ids = invoke(['docker', 'ps', '-aq', '--filter', 'name=^/' + name + '$']).strip()
    if not folder.exists():
        need(not ids, 'RESOURCE_WITHOUT_STATE')
        return
    need(not folder.is_symlink() and folder.resolve() == folder and folder.stat().st_mode & 0o077 == 0,
         'UNSAFE_RUN_DIRECTORY')
    state = json.loads((folder / 'state.json').read_text())
    need(state['name'] == name and state['token'] == token, 'CLEANUP_IDENTITY_MISMATCH')
    failures = []
    for item in reversed(state['processes']):
        try:
            stop_process(item)
        except Exception:
            failures.append('PROCESS_CLEANUP_UNCONFIRMED')
    remove_owned_container(name, token)
    need(not failures and all(port_free(p) for p in PORTS), 'LISTENER_OR_PROCESS_REMAINS')
    # A Chrome process outside the owned process group must not be hidden by
    # deleting its profile. Never kill unrelated browser/user processes.
    marker = ('--user-data-dir=' + str(folder / 'chrome-profile')).encode()
    for path in Path('/proc').glob('[0-9]*/cmdline'):
        try:
            need(marker not in path.read_bytes().split(b'\0'), 'BROWSER_REMAINS')
        except FileNotFoundError:
            pass
    shutil.rmtree(folder)
    need(not folder.exists(), 'PRIVATE_FILES_REMAIN')


def start(folder, state, args, label):
    log = os.open(folder / (label + '.log'), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        p = subprocess.Popen(args, cwd=ROOT, stdin=subprocess.DEVNULL, stdout=log, stderr=log,
                             start_new_session=True, env=runtime_env())
    finally:
        os.close(log)
    item = proc_identity(p.pid)
    need(item is not None and item['group'] == p.pid, 'PROCESS_START_FAILED')
    state['processes'].append(item)
    record(folder, state)
    return p, item


def http(path, *, origin=ORIGIN, host='127.0.0.1:5173', body=None, cookie=None, headers=None):
    need(path.startswith('/api/v1/') and '?' not in path, 'INVALID_HTTP_TARGET')
    h = {'Host': host, 'Origin': origin}
    h.update(headers or {})
    if cookie:
        h['Cookie'] = cookie
    if body is not None:
        h['Content-Type'] = 'application/json'
    request = Request('http://127.0.0.1:3101' + path, data=None if body is None else json.dumps(body).encode(), headers=h)
    opener = build_opener(ProxyHandler({}), NoRedirect())
    try:
        response = opener.open(request, timeout=5)
    except HTTPError as e:
        response = e
    with response:
        payload = response.read(65537)
        need(len(payload) <= 65536, 'HTTP_RESPONSE_LIMIT')
        return response.status, response.headers, json.loads(payload)


def await_http(process, path, status):
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        need(process.poll() is None, 'PROCESS_EXITED_EARLY')
        try:
            if http(path)[0] == status:
                return
        except (OSError, ValueError):
            pass
        time.sleep(.1)
    raise RuntimeError('HTTP_READY_TIMEOUT')


def await_service(process, url, expected):
    need(url in (ORIGIN + '/', DRIVER + '/status'), 'SERVICE_TARGET_REJECTED')
    opener = build_opener(ProxyHandler({}), NoRedirect())
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        need(process.poll() is None, 'PROCESS_EXITED_EARLY')
        try:
            with opener.open(url, timeout=2) as response:
                raw = response.read(65537)
                if response.status == 200 and len(raw) <= 65536 and expected(raw):
                    return
        except (OSError, ValueError):
            pass
        time.sleep(.1)
    raise RuntimeError('SERVICE_READY_TIMEOUT')


def encoded_password(password):
    salt = secrets.token_bytes(16)
    digest = hashlib.pbkdf2_hmac('sha256', password.encode(), salt, 600000, 32)
    return 'worktool-pbkdf2-sha256$600000$' + base64.b64encode(salt).decode() + '$' + base64.b64encode(digest).decode()


def quote(value):
    return "'" + value.replace("'", "''") + "'"


def verify_browser_binary(value):
    need(value in (CHROMIUM_BINARY, CHROMEDRIVER_BINARY), 'BROWSER_PATH_REJECTED')
    path = Path(value)
    need(path.is_absolute() and path.resolve(strict=True) == path, 'BROWSER_PATH_NONCANONICAL')
    for item in (path, *path.parents):
        need(not stat.S_ISLNK(item.lstat().st_mode), 'BROWSER_PATH_SYMLINK')
    mode = path.lstat().st_mode
    need(stat.S_ISREG(mode) and bool(mode & 0o111), 'BROWSER_NOT_EXECUTABLE')
    need(not (mode & 0o022), 'BROWSER_PERMISSION_REJECTED')
    return value


def browser_version(raw, driver=False):
    code = 'DRIVER_VERSION_REJECTED' if driver else 'BROWSER_VERSION_REJECTED'
    need(isinstance(raw, bytes) and len(raw) <= 256, code)
    number = rb'([0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6})'
    # ChromeDriver's optional suffix is a bounded commit/source reference, not
    # arbitrary text. Chromium uses --product-version to avoid the product/channel
    # separator printed by --version even when the channel is empty. No raw output is logged.
    suffix = rb'(?: \([a-f0-9]{40}(?:-refs/(?:branch-heads/[0-9]{1,8}|heads/main)@\{#[0-9]{1,9}\})?\))?'
    pattern = rb'ChromeDriver ' + number + suffix + rb'\r?\n?' if driver else number + rb'(?:\r?\n)?'
    match = re.fullmatch(pattern, raw)
    need(match is not None, code)
    return match[1]


def browser_preflight():
    # Verify BOTH files before executing EITHER version command. This happens
    # before port probes, resource creation, PostgreSQL, or service startup.
    chrome = verify_browser_binary(CHROMIUM_BINARY)
    driver = verify_browser_binary(CHROMEDRIVER_BINARY)
    cv = browser_version(invoke([chrome, '--product-version'], timeout=5))
    dv = browser_version(invoke([driver, '--version'], timeout=5), driver=True)
    need(cv.split(b'.')[:3] == dv.split(b'.')[:3], 'BROWSER_DRIVER_MISMATCH')
    return chrome, driver, cv, dv


def browser_preflight_failure(error):
    if isinstance(error, FileNotFoundError):
        return 'missing'
    if isinstance(error, PermissionError):
        return 'permission'
    if isinstance(error, subprocess.TimeoutExpired):
        return 'timeout'
    code = error.args[0] if isinstance(error, RuntimeError) and error.args else None
    return {'BROWSER_PATH_REJECTED': 'path', 'BROWSER_PATH_NONCANONICAL': 'path',
            'BROWSER_PATH_SYMLINK': 'path', 'BROWSER_NOT_EXECUTABLE': 'notexec',
            'BROWSER_PERMISSION_REJECTED': 'permission', 'BROWSER_VERSION_REJECTED': 'browser-versionformat',
            'DRIVER_VERSION_REJECTED': 'driver-versionformat',
            'BROWSER_DRIVER_MISMATCH': 'mismatch'}.get(code, 'unavailable')


def run():
    name, token, folder = identity()
    need(hashlib.sha256((ROOT / 'scripts/pg-integration-ci.py').read_bytes()).hexdigest() == GATES_SHA256,
         'EXISTING_RESOURCE_GATES_CHANGED')
    try:
        chrome, driver, cv, dv = browser_preflight()
    except Exception as error:
        print(json.dumps({'checkpoint': 'browser-preflight', 'result': 'FAIL', 'code': browser_preflight_failure(error)}))
        raise
    print(json.dumps({'checkpoint': 'browser-preflight', 'result': 'PASS',
                      'browserVersion': cv.decode('ascii'), 'driverVersion': dv.decode('ascii')}))
    need(not folder.exists() and all(port_free(p) for p in PORTS), 'RESOURCE_ALREADY_EXISTS')
    need(shutil.which('docker') and shutil.which('go') and shutil.which('node'), 'TOOL_MISSING')
    need(invoke(['go', 'env', 'GOVERSION']).strip() == b'go1.27.3', 'GO_VERSION_MISMATCH')
    need(invoke(['node', '--version']).strip() == b'v24.22.1', 'NODE_VERSION_MISMATCH')
    folder.mkdir(mode=0o700)
    state = {'name': name, 'token': token, 'processes': []}
    record(folder, state)
    completed = set()
    phases = set()
    deadline = time.monotonic() + 600
    signal.signal(signal.SIGALRM, lambda *_: (_ for _ in ()).throw(RuntimeError('BATCH_DEADLINE')))
    signal.alarm(600)
    try:
        suffix = secrets.token_hex(12)
        db, owner, store, admin = ('wt_test_' + suffix, 'wt_owner_' + suffix, 'wt_store_' + suffix, 'wt_admin_' + suffix)
        schema = 'wt_console_' + suffix
        admin_pw, owner_pw, store_pw = [secrets.token_hex(24) for _ in range(3)]
        private(folder / 'pg.env', f'POSTGRES_USER={admin}\nPOSTGRES_PASSWORD={admin_pw}\nPOSTGRES_DB={db}\nPOSTGRES_INITDB_ARGS=--auth-host=scram-sha-256\nPGDATA=/var/lib/postgresql/18/docker\n')
        progress('pg-create')
        invoke(['docker', 'pull', '--platform', 'linux/amd64', IMAGE], timeout=180)
        invoke(['docker', 'run', '-d', '--name', name, '--label', gates.LABEL + '=' + token,
                '--platform', 'linux/amd64', '--network', 'bridge', '--cpus', '2', '--memory', '1g',
                '--memory-swap', '1g', '--shm-size', '64m', '--tmpfs', '/var/lib/postgresql:rw,nosuid,nodev,size=2147483648',
                '--publish', '127.0.0.1::5432', '--env-file', str(folder / 'pg.env'), IMAGE,
                'postgres', '-c', 'max_connections=10', '-c', 'statement_timeout=5s', '-c', 'lock_timeout=1s',
                '-c', 'idle_in_transaction_session_timeout=5s', '-c', 'log_statement=none',
                '-c', 'log_min_error_statement=panic'])
        port = int(gates.verify_inspect(gates.inspect(name), name, token))
        def sql(text, role=admin):
            return invoke(['docker', 'exec', '-i', name, 'psql', '-X', '-q', '-A', '-t', '-v', 'ON_ERROR_STOP=1',
                           '-U', role, '-d', db], data=text.encode()).decode().strip()
        for _ in range(45):
            if gates.ready_probe(name, admin, db).returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError('PG_READY_TIMEOUT')
        need(sql('SHOW server_version_num;') == '180006', 'PG_VERSION_MISMATCH')
        need(sql('SHOW max_connections;') == '10', 'PG_CONNECTION_LIMIT_MISMATCH')
        for key, value in [('statement_timeout', '5s'), ('lock_timeout', '1s'), ('idle_in_transaction_session_timeout', '5s'),
                           ('log_statement', 'none'), ('log_min_error_statement', 'panic')]:
            need(sql('SHOW ' + key + ';') == value, 'PG_TIMEOUT_MISMATCH')
        need(sql('SHOW data_directory;') == '/var/lib/postgresql/18/docker', 'PG_DATA_DIRECTORY_MISMATCH')
        need(invoke(['docker', 'exec', name, 'readlink', '-f', '/var/lib/postgresql/18/docker/pg_wal']).strip()
             == b'/var/lib/postgresql/18/docker/pg_wal', 'PG_WAL_DIRECTORY_MISMATCH')
        progress('pg-seed')
        sql(f'CREATE ROLE {owner} LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD {quote(owner_pw)};'
            f'CREATE ROLE {store} LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD {quote(store_pw)};'
            f'ALTER DATABASE {db} OWNER TO {owner}; REVOKE ALL ON DATABASE {db} FROM PUBLIC; GRANT CONNECT ON DATABASE {db} TO {store};')
        sql(f'CREATE SCHEMA {schema}; REVOKE ALL ON SCHEMA {schema} FROM PUBLIC;', owner)
        for migration in MIGRATIONS:
            sql(f'BEGIN; SET search_path TO {schema},pg_catalog;\n' + (ROOT / 'backend-go/migrations' / migration).read_text() + '\nCOMMIT;', owner)
        # This browser slice grants only SELECT to its application role. Gateway
        # mutation routes have no credentials or device sessions to exercise.
        sql(f'GRANT USAGE ON SCHEMA {schema} TO {store}; GRANT SELECT ON ALL TABLES IN SCHEMA {schema} TO {store};', owner)
        accounts = {key: {'tenantId': 'tenant_b' if key == 'other' else 'tenant_a', 'loginName': key,
                          'password': secrets.token_urlsafe(24)} for key in ('admin', 'operator', 'owner', 'other')}
        users = ','.join('(' + ','.join(map(quote, [a['tenantId'], key, key, 'operator' if key == 'other' else key,
                                                  encoded_password(a['password'])])) + ')' for key, a in accounts.items())
        runtime = {'instanceId': 'instance_' + suffix, 'bootId': 'boot_' + suffix, 'recoveryEpoch': 'epoch_' + suffix}
        sql(f"SET search_path TO {schema},pg_catalog; INSERT INTO tenants VALUES('tenant_a'),('tenant_b');"
            f'INSERT INTO users(tenant,id,login,role,password) VALUES {users};'
            "INSERT INTO devices(tenant,id,name,key_id,secret,capabilities) VALUES"
            "('tenant_a','device_a1','Synthetic A1','disabled_a1','!disabled_a1','[]'),"
            "('tenant_a','device_a2','Synthetic A2','disabled_a2','!disabled_a2','[]');"
            f"INSERT INTO runtime_instances(instance_id,roles,boot_id,recovery_epoch,state) VALUES({quote(runtime['instanceId'])},"
            f"ARRAY['api','gateway'],{quote(runtime['bootId'])},{quote(runtime['recoveryEpoch'])},'active');", owner)
        def owner_sql(text):
            return sql(f'SET search_path TO {schema},pg_catalog; ' + text, owner)
        private(folder / 'pg-password', store_pw)
        def gate(opened=True, expired=False):
            value = {'version': 1, 'mode': 'isolated-synthetic', 'runtime': runtime, 'admissionOpen': opened,
                     'validUntil': (datetime.now(timezone.utc) + timedelta(seconds=-5 if expired else 240)).isoformat().replace('+00:00', 'Z')}
            private(folder / 'gate.json', json.dumps(value), replace=(folder / 'gate.json').exists())
        gate()
        config = {'version': 1, 'mode': 'isolated-synthetic', 'listen': '127.0.0.1:3101', 'browserOrigin': ORIGIN,
                  'database': {'host': '127.0.0.1', 'port': port, 'name': db, 'user': store, 'schema': schema,
                               'passwordFile': str(folder / 'pg-password')}, 'runtime': runtime, 'recoveryGateFile': str(folder / 'gate.json')}
        private(folder / 'config.json', json.dumps(config))
        fixture = {'driverOrigin': DRIVER, 'appOrigin': ORIGIN, 'accounts': accounts,
                   'expected': {'tenantADeviceIds': ['device_a1', 'device_a2'], 'tenantBDeviceIds': []},
                   'chromeBinary': str(Path(chrome).resolve()), 'profilePath': str(folder / 'chrome-profile')}
        private(folder / 'fixture.json', json.dumps(fixture))
        # Build from the exact checked-out candidate, never from an earlier artifact.
        progress('build')
        invoke(['go', '-C', str(ROOT / 'backend-go'), 'build', '-trimpath', '-o', str(folder / 'worktool-server'), './cmd/worktool-server'], timeout=120)
        progress('driver-start')
        driver_process, _ = start(folder, state, [driver, '--port=9515', '--allowed-ips=127.0.0.1', '--log-level=OFF'], 'driver')
        await_service(driver_process, DRIVER + '/status', lambda raw: json.loads(raw).get('value', {}).get('ready') is True)
        progress('app-start')
        app, app_item = start(folder, state, [str(folder / 'worktool-server'), '--config', str(folder / 'config.json'), '--serve'], 'app')
        await_http(app, '/api/v1/auth/me', 401)
        progress('frontend-start')
        frontend, _ = start(folder, state, [shutil.which('node'), str(ROOT / 'node_modules/vite/bin/vite.js'), '--config', 'console/vite.config.ts', '--mode', 'go', '--host', '127.0.0.1'], 'frontend')
        await_service(frontend, ORIGIN + '/', lambda raw: b'src/main.tsx' in raw)
        completed.add('startup')
        def phase(value):
            need(value in PHASES and value not in phases and time.monotonic() < deadline, 'PHASE_OR_DEADLINE_REJECTED')
            phases.add(value)
            progress(value)
            process = gates.invoke(['node', str(ROOT / 'qa/go-console-browser-check.ts'), '--phase', value, '--fixture', str(folder / 'fixture.json')], timeout=50 if value == 'initial' else 25, env=runtime_env(), check=False)
            if process.returncode != 0:
                print(json.dumps(browser_diagnostic(process.stdout, value)))
                raise RuntimeError('BROWSER_PHASE_FAILED')
            need(json.loads(process.stdout) == {'phase': value, 'result': 'PASS'}, 'BROWSER_PHASE_FAILED')
        # Boundary checks perform no credential disclosure and no redirects.
        progress('http-boundaries')
        need(http('/api/v1/auth/me', origin='http://127.0.0.1:9')[0] == 403, 'ORIGIN_BOUNDARY_FAILED')
        need(http('/api/v1/auth/me', host='127.0.0.1:9')[0] == 403, 'HOST_BOUNDARY_FAILED')
        need(http('/api/v1/auth/me', headers={'Cookie': 'worktool_go_console=bad; worktool_go_console=bad'})[0] == 400, 'COOKIE_BOUNDARY_FAILED')
        status, headers, auth = http('/api/v1/auth/login', body=accounts['owner'])
        need(status == 200 and auth.get('user', {}).get('role') == 'owner', 'OWNER_LOGIN_FAILED')
        owner_cookie = headers.get('Set-Cookie', '').split(';', 1)[0]
        need(owner_cookie.startswith('worktool_go_console=') and auth.get('csrfToken'), 'OWNER_SESSION_MISSING')
        need(http('/api/v1/devices', cookie=owner_cookie)[0] == 403, 'BACKEND_OWNER_ROLE_BOUNDARY_FAILED')
        need(http('/api/v1/auth/logout', cookie=owner_cookie, body={}, headers={'X-CSRF-Token': auth['csrfToken']})[0] == 200,
             'OWNER_LOGOUT_FAILED')
        need(http('/api/v1/auth/me', cookie=owner_cookie)[0] == 401, 'SERVER_SESSION_NOT_REVOKED')
        completed.add('http-boundaries')
        phase('initial'); completed.add('browser-initial')
        owner_sql("UPDATE users SET active=false WHERE tenant='tenant_a' AND id='admin';")
        phase('revoked'); completed.add('user-revocation')
        owner_sql("UPDATE users SET active=true WHERE tenant='tenant_a' AND id='admin';")
        phase('gate-prepare')
        progress('runtime-revocation')
        owner_sql("UPDATE runtime_instances SET state='draining';")
        # Login reads runtime authority before password derivation.
        need(http('/api/v1/auth/login', body=accounts['admin'])[0] == 503, 'RUNTIME_REVOCATION_FAILED')
        owner_sql("UPDATE runtime_instances SET state='active';"); completed.add('runtime-revocation')
        gate(False); phase('gate-closed'); completed.add('closed-gate')
        progress('expired-gate')
        gate(True, True)
        need(http('/api/v1/auth/login', body=accounts['admin'])[0] == 503, 'EXPIRED_GATE_FAILED')
        completed.add('expired-gate'); gate()
        phase('restart-prepare')
        progress('app-restart')
        need(stop_process(app_item, graceful=True) == 0, 'APP_SHUTDOWN_NONZERO')
        state['processes'].remove(app_item); record(folder, state)
        app, app_item = start(folder, state, [str(folder / 'worktool-server'), '--config', str(folder / 'config.json'), '--serve'], 'app-restarted')
        await_http(app, '/api/v1/auth/me', 401)
        phase('restarted'); completed.add('browser-restart')
        progress('read-only-database')
        need(owner_sql('SELECT (SELECT count(*) FROM browser_sessions)+(SELECT count(*) FROM device_sessions)+(SELECT count(*) FROM commands)+(SELECT count(*) FROM callback_outbox);') == '0', 'UNEXPECTED_DATABASE_WRITES')
        completed.add('read-only-database')
        phase('cleanup')
        progress('shutdown')
        need(stop_process(app_item, graceful=True) == 0, 'APP_SHUTDOWN_NONZERO')
        state['processes'].remove(app_item); record(folder, state)
    finally:
        signal.alarm(0)
        if sys.exc_info()[0] is not None:
            print(json.dumps({'status': 'FAIL', 'stage': stage, 'completedCases': sorted(completed & CASES)}))
        cleanup()
    completed.add('cleanup')
    need(completed == CASES, 'MISSING_CASE')
    print(json.dumps({'status': 'PASS', 'cases': sorted(completed), 'pgImage': IMAGE,
                      'chromeVersion': cv.decode(), 'driverVersion': dv.decode(),
                      'limits': {'pgCPUs': 2, 'pgMemoryBytes': 1073741824, 'pgMemorySwapBytes': 1073741824,
                                 'pgTmpfsBytes': 2147483648, 'jobMinutes': 15, 'operationSeconds': 600},
                      'noRealDevices': True, 'noExternalCallbacks': True, 'cleanupConfirmed': True}))


def browser_preflight_selftest():
    from types import SimpleNamespace
    from contextlib import redirect_stdout
    from io import StringIO
    calls = []
    def metadata(path):
        return SimpleNamespace(st_mode=(stat.S_IFREG | 0o755) if str(path) in (CHROMIUM_BINARY, CHROMEDRIVER_BINARY) else (stat.S_IFDIR | 0o755))
    def version_invoke(args, **kw):
        calls.append((args, kw))
        return b'145.0.7584.0\n' if args[0] == CHROMIUM_BINARY else b'ChromeDriver 145.0.7584.1 (' + b'a' * 40 + b'-refs/heads/main@{#12345})\n'
    def no_resources(*args, **kw):
        raise AssertionError('resource reached before failed browser preflight')
    def rejected(operation):
        try:
            operation()
        except (RuntimeError, OSError) as error:
            return error
        raise AssertionError('unsafe browser preflight accepted')
    with patch.object(Path, 'resolve', lambda p, **kw: p), patch.object(Path, 'lstat', metadata), \
         patch.dict(globals(), {'invoke': version_invoke, 'port_free': no_resources, 'start': no_resources}):
        assert browser_preflight() == (CHROMIUM_BINARY, CHROMEDRIVER_BINARY, b'145.0.7584.0', b'145.0.7584.1')
        assert calls == [([CHROMIUM_BINARY, '--product-version'], {'timeout': 5}),
                         ([CHROMEDRIVER_BINARY, '--version'], {'timeout': 5})]
        # Counterexample to the old parser: official --version formatting can
        # include a trailing separator for an empty channel, which it rejected.
        old_pattern = rb'Chromium ([0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6})\r?\n?'
        assert re.fullmatch(old_pattern, b'Chromium 145.0.7584.0 \n') is None
        for raw in (b'145.0.7584.0', b'145.0.7584.0\n', b'145.0.7584.0\r\n'):
            assert browser_version(raw) == b'145.0.7584.0'
        for binary in (CHROMIUM_BINARY, CHROMEDRIVER_BINARY):
            for mode in (stat.S_IFREG | 0o777, stat.S_IFREG | 0o775, stat.S_IFREG | 0o757,
                         stat.S_IFREG | 0o644, stat.S_IFDIR | 0o755, stat.S_IFLNK | 0o777):
                calls.clear()
                with patch.object(Path, 'lstat', lambda p, b=binary, m=mode: SimpleNamespace(st_mode=m) if str(p) == b else metadata(p)):
                    rejected(browser_preflight)
                assert not calls
            calls.clear()
            def missing(p):
                if str(p) == binary:
                    raise FileNotFoundError()
                return metadata(p)
            with patch.object(Path, 'lstat', missing):
                rejected(browser_preflight)
            assert not calls
        calls.clear()
        with patch.object(Path, 'resolve', lambda p, **kw: Path('/different') if str(p) == CHROMIUM_BINARY else p):
            rejected(browser_preflight)
        assert not calls
        with patch.object(Path, 'lstat', lambda p: SimpleNamespace(st_mode=stat.S_IFLNK | 0o777) if str(p) == '/usr/local/share' else metadata(p)):
            rejected(browser_preflight)
        assert not calls
        for raw in (b'', b'Google Chrome 145.0.7584.0\n', b'Chromium 145.0.7584.0 \n',
                    b'145.0.7584.0 extra', b'145.0.7584.0 \n', b'145.0.7584.0\r',
                    b'145.0.7584', b'145.0.7584.0.1', b'145.a.7584.0', b'145.0.7584.0\nextra',
                    b'145.0.7584.0\n\n', b'1234567.0.7584.0', b'x' * 257):
            calls.clear()
            def bad_version(args, **kw):
                calls.append(args)
                return raw
            with patch.dict(globals(), {'invoke': bad_version}):
                error = rejected(browser_preflight)
            assert browser_preflight_failure(error) == 'browser-versionformat'
            assert calls == [[CHROMIUM_BINARY, '--product-version']]
        calls.clear()
        def mismatch(args, **kw):
            calls.append(args)
            return b'145.0.7584.0\n' if args[0] == CHROMIUM_BINARY else b'ChromeDriver 146.0.7584.0\n'
        with patch.dict(globals(), {'invoke': mismatch, 'identity': lambda: ('synthetic', 'synthetic', Path('/synthetic'))}):
            with redirect_stdout(StringIO()) as output:
                error = rejected(run)
            assert json.loads(output.getvalue())['code'] == 'mismatch'
        assert browser_preflight_failure(error) == 'mismatch'
        assert calls == [[CHROMIUM_BINARY, '--product-version'], [CHROMEDRIVER_BINARY, '--version']]
        for raw in (b'ChromeDriver 145.0.7584.0 SECRET', b'prefix ChromeDriver 145.0.7584.0', b'ChromeDriver 145.0.7584.0 (unknown)'):
            error = rejected(lambda: browser_version(raw, driver=True))
            assert browser_preflight_failure(error) == 'driver-versionformat'
        calls.clear()
        def bad_driver(args, **kw):
            calls.append(args)
            return b'145.0.7584.0\n' if args[0] == CHROMIUM_BINARY else b'ChromeDriver malformed SECRET'
        with patch.dict(globals(), {'invoke': bad_driver, 'identity': lambda: ('synthetic', 'synthetic', Path('/synthetic'))}):
            with redirect_stdout(StringIO()) as output:
                rejected(run)
            assert json.loads(output.getvalue()) == {'checkpoint': 'browser-preflight', 'result': 'FAIL', 'code': 'driver-versionformat'}
            assert 'SECRET' not in output.getvalue()
        assert calls == [[CHROMIUM_BINARY, '--product-version'], [CHROMEDRIVER_BINARY, '--version']]
        # Run-level ordering: an unsafe binary must fail before even port probes.
        calls.clear()
        with patch.dict(globals(), {'identity': lambda: ('synthetic', 'synthetic', Path('/synthetic'))}), \
             patch.object(Path, 'lstat', lambda p: SimpleNamespace(st_mode=stat.S_IFREG | 0o777)):
            with redirect_stdout(StringIO()) as output:
                rejected(run)
            assert json.loads(output.getvalue()) == {'checkpoint': 'browser-preflight', 'result': 'FAIL', 'code': 'permission'}
        assert not calls
        assert browser_preflight_failure(RuntimeError('SECRET')) == 'unavailable'
        assert browser_preflight_failure(FileNotFoundError('SECRET')) == 'missing'
        assert browser_preflight_failure(subprocess.TimeoutExpired(['SECRET'], 5)) == 'timeout'


def selftest():
    # No subprocess, credential generation, filesystem mutation or network.
    browser_preflight_selftest()
    original = gates.invoke
    original_invoke, original_inspect = globals()['invoke'], gates.inspect
    gates.invoke = lambda *a, **kw: (_ for _ in ()).throw(AssertionError('unexpected subprocess'))
    try:
        good = {'GITHUB_ACTIONS': 'true', 'RUNNER_ENVIRONMENT': 'github-hosted', 'RUNNER_OS': 'Linux',
                'GITHUB_REPOSITORY': 'ZhangIvan/worktool-open', 'GITHUB_REF': 'refs/heads/worktool/synthetic-prototype',
                'GITHUB_EVENT_NAME': 'push', 'WORKTOOL_CONSOLE_CI_ALLOW': ALLOW, 'GITHUB_RUN_ID': '123',
                'GITHUB_RUN_ATTEMPT': '1', 'RUNNER_TEMP': '/tmp'}
        assert identity(good)[0] == 'wt-console-123-1'
        for key in good:
            bad = dict(good); bad[key] = 'unsafe'
            try:
                identity(bad)
            except (RuntimeError, ValueError):
                pass
            else:
                raise AssertionError('unsafe identity accepted')
        assert quote("a'b") == "'a''b'"
        assert NoRedirect().redirect_request(None) is None
        good_diagnostic = {'phase': 'initial', 'result': 'FAIL', 'diagnostic': {'checkpoint': 'session-create',
                           'category': 'webdriver', 'actor': 'none', 'httpStatus': 500, 'webdriverError': 'session not created'}}
        assert browser_diagnostic(json.dumps(good_diagnostic), 'initial') == good_diagnostic
        for key, value in [('message', 'secret-do-not-emit'), ('webdriverError', 'secret-do-not-emit'),
                           ('checkpoint', 'secret-do-not-emit'), ('httpStatus', 'secret-do-not-emit')]:
            bad = json.loads(json.dumps(good_diagnostic)); bad['diagnostic'][key] = value
            assert 'secret-do-not-emit' not in json.dumps(browser_diagnostic(json.dumps(bad), 'initial'))
        assert hashlib.sha256((ROOT / 'scripts/pg-integration-ci.py').read_bytes()).hexdigest() == GATES_SHA256
        assert runtime_env()['NODE_USE_ENV_PROXY'] == '0'
        assert len(MIGRATIONS) == 8 and len(CASES) == 10 and len(PHASES) == 7
        gates.selftest()
        calls = []
        def fake_invoke(args, **_):
            calls.append(args)
            return b'owned-id' if len(calls) == 1 else b''
        globals()['invoke'] = fake_invoke
        gates.inspect = lambda name: {'name': '/' + name, 'label': '123-1', 'memory': 0, 'privileged': True}
        remove_owned_container('wt-console-123-1', '123-1')
        assert calls[1] == ['docker', 'rm', '-f', 'wt-console-123-1'] and len(calls) == 3
        calls.clear()
        gates.inspect = lambda name: {'name': '/' + name, 'label': 'foreign'}
        try:
            remove_owned_container('wt-console-123-1', '123-1')
        except RuntimeError:
            pass
        else:
            raise AssertionError('foreign resource cleanup accepted')
        assert len(calls) == 1
        item = {'pid': 123, 'start': '1', 'group': 123, 'cmdHash': 'synthetic'}
        # The process can disappear before the loop's first condition. Its
        # exit status must still be captured, and nonzero must remain nonzero.
        for status, expected in ((0, 0), (7 << 8, 7)):
            signals = []
            with patch.dict(globals(), {'proc_identity': lambda _: item, 'group_members': lambda _: []}), \
                 patch.object(os, 'killpg', lambda pid, sig: signals.append((pid, sig))), \
                 patch.object(os, 'waitpid', lambda *_: (123, status)):
                assert stop_process(item, graceful=True) == expected
                assert signals == [(123, signal.SIGTERM)]
        with patch.dict(globals(), {'proc_identity': lambda _: dict(item, start='other')}), \
             patch.object(os, 'killpg', lambda *_: (_ for _ in ()).throw(AssertionError('foreign kill'))):
            try:
                stop_process(item)
            except RuntimeError:
                pass
            else:
                raise AssertionError('foreign process accepted')
    finally:
        gates.invoke = original
        globals()['invoke'], gates.inspect = original_invoke, original_inspect
    print('PASS console harness pure guards; browser/PG/services NOT_RUN')


if __name__ == '__main__':
    os.umask(0o077)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    signal.signal(signal.SIGINT, lambda *_: sys.exit(130))
    try:
        need(len(sys.argv) == 2 and sys.argv[1] in ('run', 'cleanup', '--selftest'), 'INVALID_ACTION')
        {'run': run, 'cleanup': cleanup, '--selftest': selftest}[sys.argv[1]]()
    except (Exception, KeyboardInterrupt):
        # Error strings, psql, browser and child process output may contain secrets.
        print('CONSOLE_CI_FAILED_REDACTED', file=sys.stderr)
        sys.exit(1)
