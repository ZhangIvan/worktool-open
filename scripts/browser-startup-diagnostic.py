#!/usr/bin/env python3
"""Hosted-only single-session startup diagnostic; --selftest has no runtime actions."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import subprocess
import sys
import time
from urllib.error import HTTPError, URLError
from urllib.request import Request, build_opener, ProxyHandler
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
HARNESS_SHA = 'e0f8898d2e01270a41318a0756155142ee5dd91c00c7b04f6cca7e723361b7ba'
PG_SHA = '88c1ff0e5f63468e883ca96b4a7d35602110d6a21d21f9ea36c093841ee157bf'
DRIVER = 'http://127.0.0.1:9515'
ALLOW = 'single-synthetic-browser-startup-only'
ERRORS = frozenset(('session not created|invalid argument|invalid session id|unknown error|unknown command|unknown method|unsupported operation|timeout|script timeout|no such window|unexpected alert open').split('|'))


def require(value):
    if not value:
        raise Failure('validation')


class Failure(Exception):
    def __init__(self, code, http=None, driver_error=None):
        super().__init__('DIAGNOSTIC_FAILED')
        self.code = code
        self.http = http if type(http) is int and 100 <= http <= 599 else None
        self.driver_error = driver_error if driver_error in ERRORS else None


def harness():
    for relative, expected in [('scripts/go-console-integration-ci.py', HARNESS_SHA), ('scripts/pg-integration-ci.py', PG_SHA)]:
        require(hashlib.sha256((ROOT / relative).read_bytes()).hexdigest() == expected)
    spec = importlib.util.spec_from_file_location('verified_console_harness', ROOT / 'scripts/go-console-integration-ci.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def no_links(path):
    require(path.is_absolute() and path.resolve(strict=True) == path)
    for item in (path, *path.parents):
        require(not stat.S_ISLNK(item.lstat().st_mode))


def identity(env=None):
    e = os.environ if env is None else env
    require(e.get('GITHUB_ACTIONS') == 'true' and e.get('RUNNER_ENVIRONMENT') == 'github-hosted'
            and e.get('RUNNER_OS') == 'Linux' and e.get('GITHUB_REPOSITORY') == 'ZhangIvan/worktool-open'
            and e.get('GITHUB_REF') == 'refs/heads/worktool/synthetic-prototype' and e.get('GITHUB_EVENT_NAME') == 'push'
            and e.get('WORKTOOL_BROWSER_STARTUP_ALLOW') == ALLOW)
    run, attempt = e.get('GITHUB_RUN_ID', ''), e.get('GITHUB_RUN_ATTEMPT', '')
    require(re.fullmatch(r'[0-9]{1,20}', run) and re.fullmatch(r'[0-9]{1,5}', attempt))
    root = Path(e.get('RUNNER_TEMP', ''))
    no_links(root)
    token = run + '-' + attempt
    return token, root / ('wt-browser-startup-' + token)


def private_directory(folder):
    no_links(folder)
    s = folder.lstat()
    require(stat.S_ISDIR(s.st_mode) and s.st_uid == os.getuid() and stat.S_IMODE(s.st_mode) == 0o700)


def read_state(folder, token):
    private_directory(folder)
    path = folder / 'state.json'
    no_links(path)
    before = path.lstat()
    require(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and before.st_uid == os.getuid()
            and stat.S_IMODE(before.st_mode) == 0o600 and before.st_size <= 8192)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        opened = os.fstat(fd)
        require((before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns, before.st_ctime_ns) ==
                (opened.st_dev, opened.st_ino, opened.st_size, opened.st_mtime_ns, opened.st_ctime_ns))
        raw = os.read(fd, 8193)
        after = os.fstat(fd)
        stable = lambda item: (item.st_dev, item.st_ino, item.st_size, item.st_mtime_ns, item.st_ctime_ns, item.st_mode, item.st_uid, item.st_nlink)
        require(len(raw) == opened.st_size <= 8192 and stable(after) == stable(opened) and stable(path.lstat()) == stable(before))
    finally:
        os.close(fd)
    state = json.loads(raw)
    validate_state(state, token)
    return state


def validate_state(state, token):
    require(isinstance(state, dict) and set(state) <= {'token', 'processes', 'sessionId'}
            and state.get('token') == token and isinstance(state.get('processes'), list) and len(state['processes']) <= 1)
    for item in state['processes']:
        require(isinstance(item, dict) and set(item) == {'pid', 'start', 'group', 'cmdHash'}
                and type(item['pid']) is int and 1 < item['pid'] < 2**31 and item['group'] == item['pid']
                and isinstance(item['start'], str) and re.fullmatch(r'[0-9]{1,24}', item['start'])
                and isinstance(item['cmdHash'], str) and re.fullmatch(r'[a-f0-9]{64}', item['cmdHash']))
    if 'sessionId' in state:
        session_id(state['sessionId'])


def session_id(value):
    require(isinstance(value, str) and re.fullmatch(r'[A-Za-z0-9._-]{1,256}', value))
    return value


def target(method, path):
    require((method, path) in (('GET', '/status'), ('POST', '/session')) or
            (method == 'DELETE' and re.fullmatch(r'/session/[A-Za-z0-9._-]{1,256}', path)))
    return DRIVER + path


def decode(status, raw):
    require(type(status) is int and 100 <= status <= 599)
    if len(raw) > 131072:
        raise Failure('malformed-response', status)
    try:
        data = json.loads(raw)
    except (ValueError, TypeError):
        raise Failure('malformed-response', status) from None
    if not isinstance(data, dict) or 'value' not in data:
        raise Failure('malformed-response', status)
    value = data['value']
    if isinstance(value, dict) and 'error' in value:
        raise Failure('webdriver', status, value['error'] if isinstance(value['error'], str) else None)
    if not 200 <= status < 300:
        raise Failure('http', status)
    return value


def request(h, method, path, body=None, timeout=5):
    url = target(method, path)
    require(timeout == (25 if method == 'POST' else 5))
    opener = build_opener(ProxyHandler({}), h.NoRedirect())
    req = Request(url, method=method, headers={'Content-Type': 'application/json'},
                  data=None if body is None else json.dumps(body).encode())
    try:
        try:
            response = opener.open(req, timeout=timeout)
        except HTTPError as error:
            response = error
        with response:
            return decode(response.status, response.read(131073))
    except (TimeoutError, URLError) as error:
        timed_out = isinstance(error, TimeoutError) or isinstance(getattr(error, 'reason', None), TimeoutError)
        raise Failure('timeout' if timed_out else 'transport') from None


def capabilities(h, folder):
    # Flags intentionally match the existing UI regression; sandbox stays enabled.
    return {'capabilities': {'alwaysMatch': {'browserName': 'chrome', 'acceptInsecureCerts': False,
        'pageLoadStrategy': 'eager', 'timeouts': {'implicit': 0, 'pageLoad': 5000, 'script': 5000},
        'goog:chromeOptions': {'binary': h.CHROMIUM_BINARY, 'browserStartupTimeout': 20000,
            'args': ['--headless=new', '--no-proxy-server', '--window-size=1280,900', '--disable-extensions',
                     '--disable-background-networking', '--disable-sync', '--disable-component-update',
                     '--disable-default-apps', '--no-first-run', '--no-default-browser-check', '--metrics-recording-only',
                     '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1', '--user-data-dir=' + str(folder / 'chrome-profile')],
            'prefs': {'credentials_enable_service': False, 'profile.password_manager_enabled': False}}}}}


def listener_rows(lines, ipv6, owned_inodes):
    count = 0
    loopback = '00000000000000000000000001000000' if ipv6 else '0100007F'
    for line in lines[1:]:
        fields = line.split()
        if len(fields) < 10 or fields[3] != '0A':
            continue
        address, port = fields[1].split(':')
        if port.upper() != format(9515, '04X'):
            continue
        require(address.upper() == loopback and fields[9] in owned_inodes)
        count += 1
    return count


def verify_listeners(h, item):
    require(h.proc_identity(item['pid']) == item)
    proc = Path('/proc') / str(item['pid'])
    inodes = set()
    for fd in (proc / 'fd').iterdir():
        try:
            match = re.fullmatch(r'socket:\[([0-9]+)\]', os.readlink(fd))
            if match:
                inodes.add(match[1])
        except FileNotFoundError:
            pass
    count = 0
    for name, ipv6 in (('tcp', False), ('tcp6', True)):
        path = proc / 'net' / name
        try:
            count += listener_rows(path.read_text().splitlines(), ipv6, inodes)
        except FileNotFoundError:
            require(ipv6)  # IPv6 may be disabled; IPv4 metadata must exist.
    require(count > 0 and h.proc_identity(item['pid']) == item)


def profile_clear(folder):
    marker = ('--user-data-dir=' + str(folder / 'chrome-profile')).encode()
    for path in Path('/proc').glob('[0-9]*/cmdline'):
        try:
            require(marker not in path.read_bytes().split(b'\0'))
        except FileNotFoundError:
            pass
    return True


def launch_driver(h, folder, state):
    # Persist an uncertainty barrier BEFORE spawning. Cleanup must retain private
    # files until registration or verified emergency cleanup removes this marker.
    pending = folder / 'driver-registration-pending'
    h.private(pending, 'pending')
    previous = {}
    deferred = []
    process = item = None
    log = None
    def defer(signum, _frame):
        deferred.append(signum)
    try:
        # Parent-only exception deferral: no blocked mask is inherited by the
        # driver/browser, and no preexec_fn is used in the child.
        for signum in (signal.SIGTERM, signal.SIGALRM):
            previous[signum] = signal.getsignal(signum)
            signal.signal(signum, defer)
        log = os.open(folder / 'driver.log', os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        process = subprocess.Popen([h.CHROMEDRIVER_BINARY, '--port=9515', '--log-level=OFF'], cwd=ROOT,
                                   stdin=subprocess.DEVNULL, stdout=log, stderr=log,
                                   start_new_session=True, env=h.runtime_env())
        item = h.proc_identity(process.pid)
        require(item is not None and item['pid'] == process.pid and item['group'] == process.pid)
        state['processes'].append(item)
        h.record(folder, state)
        pending.unlink()
        return process, item
    except BaseException:
        confirmed = False
        if process is not None:
            try:
                if item is not None and item['pid'] == process.pid and item['group'] == process.pid:
                    # Only a matching recorded identity authorizes group signals.
                    h.stop_process(item)
                    process.wait(timeout=2)
                    confirmed = not h.group_members(process.pid)
                else:
                    # The unreaped Popen child is ours, but without /proc identity
                    # we cannot establish group ownership or absence of descendants.
                    if process.poll() is None:
                        process.terminate()
                    try:
                        process.wait(timeout=2)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=2)
            except BaseException:
                confirmed = False
        if confirmed:
            pending.unlink(missing_ok=True)
            raise Failure('registration-failed') from None
        # Keep the barrier for the independent cleanup step too. Never infer
        # that an empty persisted process list means no child was spawned.
        raise Failure('registration-unconfirmed') from None
    finally:
        try:
            if log is not None:
                os.close(log)
        finally:
            try:
                if signal.SIGTERM in previous:
                    signal.signal(signal.SIGTERM, previous[signal.SIGTERM])
            finally:
                if signal.SIGALRM in previous:
                    signal.signal(signal.SIGALRM, previous[signal.SIGALRM])
            if deferred:
                # Registration/emergency cleanup has stabilized ownership; the
                # normal finally-cleanup path can now handle the interruption.
                raise Failure('deadline')


def cleanup(h, token, folder):
    result = {'sessionDeleted': False, 'processesStopped': False, 'profileProcessesGone': False,
              'portFree': False, 'privateFilesGone': False}
    if folder.exists() or folder.is_symlink():
        private_directory(folder)
        pending = folder / 'driver-registration-pending'
        require(not pending.exists() and not pending.is_symlink())
        state = read_state(folder, token)  # Refuse foreign state before any destructive action.
        # Validate all recorded ownership before DELETE, kill, or private-file removal.
        for item in state['processes']:
            current = h.proc_identity(item['pid'])
            require(current == item or (current is None and not h.group_members(item['pid'])))
        if 'sessionId' in state and state['processes'] and h.proc_identity(state['processes'][0]['pid']) == state['processes'][0]:
            try:
                request(h, 'DELETE', '/session/' + session_id(state['sessionId']))
                result['sessionDeleted'] = True
            except Exception:
                pass  # A lost DELETE response still requires process-group cleanup.
        for item in state['processes']:
            h.stop_process(item)
    result['processesStopped'] = True
    result['profileProcessesGone'] = profile_clear(folder)
    result['portFree'] = h.port_free(9515)
    require(result['portFree'])
    if folder.exists():
        private_directory(folder)
        require(shutil.rmtree.avoids_symlink_attacks)
        shutil.rmtree(folder)
    result['privateFilesGone'] = not folder.exists() and not folder.is_symlink()
    require(result['privateFilesGone'])
    return result


def session_cycle(h, folder, state, checkpoint):
    checkpoint('session-create')
    value = request(h, 'POST', '/session', capabilities(h, folder), timeout=25)
    require(isinstance(value, dict))
    state['sessionId'] = session_id(value.get('sessionId'))
    h.record(folder, state)
    checkpoint('session-delete')
    request(h, 'DELETE', '/session/' + state['sessionId'])
    del state['sessionId']
    h.record(folder, state)


def timeout_signal(*_):
    raise Failure('deadline')


def execute(action):
    started = time.monotonic()
    checkpoint = 'guard'
    h = None
    token = folder = None
    outcome = None
    cleanup_result = {'sessionDeleted': False, 'processesStopped': False, 'profileProcessesGone': False, 'portFree': False, 'privateFilesGone': False}
    def mark(value):
        nonlocal checkpoint
        checkpoint = value
    signal.signal(signal.SIGALRM, timeout_signal)
    signal.signal(signal.SIGTERM, timeout_signal)
    signal.alarm(50 if action == 'run' else 25)
    try:
        token, folder = identity()
        h = harness()
        if action == 'cleanup':
            mark('cleanup')
        if action == 'run':
            mark('preflight')
            h.browser_preflight()
            require(not folder.exists() and not folder.is_symlink() and h.port_free(9515))
            folder.mkdir(mode=0o700)
            private_directory(folder)
            state = {'token': token, 'processes': []}
            h.record(folder, state)
            (folder / 'chrome-profile').mkdir(mode=0o700)
            mark('driver-start')
            # Omitting allowed-ips preserves ChromeDriver's default local-only bind.
            process, item = launch_driver(h, folder, state)
            mark('driver-ready')
            ready = False
            for _ in range(20):
                if process.poll() is not None:
                    raise Failure('driver-exited')
                try:
                    value = request(h, 'GET', '/status')
                    if isinstance(value, dict) and value.get('ready') is True:
                        ready = True
                        break
                except Failure as error:
                    if error.code not in ('transport', 'timeout'):
                        raise
                time.sleep(.1)
            require(ready)
            mark('driver-listeners')
            verify_listeners(h, item)
            session_cycle(h, folder, state, mark)
            cleanup_result['sessionDeleted'] = True
    except Exception as error:
        outcome = error if isinstance(error, Failure) else Failure('unavailable')
    finally:
        if h is not None and folder is not None:
            # 50s work + 25s cleanup is a target, not a strict inner bound:
            # critical OS spawn/file I/O can defer exceptions. The job's 2min
            # timeout is the outer bound; exhausted budgets fail closed.
            remaining = max(1, min(25, int(75 - (time.monotonic() - started))))
            signal.alarm(remaining)
            try:
                confirmed = cleanup(h, token, folder)
                confirmed['sessionDeleted'] |= cleanup_result['sessionDeleted']
                cleanup_result = confirmed
            except Exception:
                if outcome is None:
                    outcome = Failure('cleanup')
        signal.alarm(0)
    result = {'status': 'FAIL' if outcome else 'PASS', 'checkpoint': checkpoint,
              'elapsedMs': min(120000, max(0, int((time.monotonic() - started) * 1000))), 'cleanup': cleanup_result}
    if outcome:
        result['code'] = outcome.code
        if outcome.http is not None:
            result['httpStatus'] = outcome.http
        if outcome.driver_error is not None:
            result['webdriverError'] = outcome.driver_error
    print(json.dumps(result, separators=(',', ':')))
    return 1 if outcome else 0


def selftest():
    from types import SimpleNamespace
    h = harness()  # Verified source import only; never invoke its run/cleanup.
    require(decode(200, b'{"value":{"sessionId":"synthetic"}}') == {'sessionId': 'synthetic'})
    for status, raw, expected in [(500, b'{"value":{"error":"session not created","message":"SECRET"}}', 'webdriver'),
                                  (503, b'{"value":null}', 'http'), (200, b'not-json-SECRET', 'malformed-response'),
                                  (200, b'{}', 'malformed-response'), (200, b'x' * 131073, 'malformed-response')]:
        try:
            decode(status, raw)
            raise AssertionError('unsafe response accepted')
        except Failure as error:
            require(error.code == expected and 'SECRET' not in str(error))
    try:
        decode(500, b'{"value":{"error":"SECRET","message":"SECRET"}}')
    except Failure as error:
        require(error.driver_error is None)
    for method, path in [('POST', '/session/x/url'), ('GET', '/session/x/url'), ('DELETE', '/session/../x'), ('GET', 'http://other/status')]:
        try:
            target(method, path)
            raise AssertionError('forbidden action accepted')
        except Failure:
            pass
    folder = Path('/synthetic-private')
    calls = []
    def fake_request(_h, method, path, body=None, timeout=5):
        calls.append((method, path, timeout))
        return {'sessionId': 'synthetic'} if method == 'POST' else None
    recorded = []
    stub = SimpleNamespace(CHROMIUM_BINARY=h.CHROMIUM_BINARY, record=lambda *args: recorded.append(True))
    with patch.dict(globals(), request=fake_request):
        state = {'token': '1-1', 'processes': []}
        session_cycle(stub, folder, state, lambda _: None)
        require(calls == [('POST', '/session', 25), ('DELETE', '/session/synthetic', 5)] and 'sessionId' not in state)
    item = {'pid': 123, 'group': 123, 'start': '1', 'cmdHash': 'a' * 64}
    state = {'token': '1-1', 'processes': [item]}
    stopped = []
    stub.proc_identity = lambda _: item
    stub.group_members = lambda _: []
    stub.stop_process = lambda value: stopped.append(value)
    stub.port_free = lambda _: True
    exists = [True]
    def remove(_):
        exists[0] = False
    remove.avoids_symlink_attacks = True
    with patch.object(Path, 'exists', lambda p: exists[0] if p == folder else False), patch.object(Path, 'is_symlink', lambda _: False), \
         patch.dict(globals(), read_state=lambda *_: state, private_directory=lambda _: None, profile_clear=lambda _: True), \
         patch.object(shutil, 'rmtree', remove):
        # A create timeout supplies no session ID, but verified processes/files are cleaned.
        def timed_out(*args, **kwargs):
            raise Failure('timeout')
        with patch.dict(globals(), request=timed_out):
            try:
                session_cycle(stub, folder, state, lambda _: None)
            except Failure:
                pass
            require(cleanup(stub, '1-1', folder)['privateFilesGone'] and stopped == [item])
        # Missing profile/folder is an idempotent cleanup, with no DELETE or kill.
        stopped.clear()
        require(cleanup(stub, '1-1', folder)['portFree'] and not stopped)
        exists[0] = True
        stub.proc_identity = lambda _: dict(item, start='foreign')
        try:
            cleanup(stub, '1-1', folder)
            raise AssertionError('foreign PID accepted')
        except Failure:
            require(not stopped and exists[0])
    try:
        validate_state(state, 'foreign-token')
        raise AssertionError('foreign state accepted')
    except Failure:
        pass
    # Exercise execute's finally path with no actual files, signals, processes,
    # versions, sockets, or network. Successful cleanup must not erase failures.
    from contextlib import redirect_stdout
    from io import StringIO
    complete_cleanup = {'sessionDeleted': False, 'processesStopped': True, 'profileProcessesGone': True,
                        'portFree': True, 'privateFilesGone': True}
    for case in ('success', 'create-timeout', 'driver-exited', 'http-error'):
        cleanup_calls = []
        launch_calls = []
        def fake_start(_h, _folder, _state):
            launch_calls.append([_h.CHROMEDRIVER_BINARY, '--port=9515', '--log-level=OFF'])
            return SimpleNamespace(poll=lambda: 7 if case == 'driver-exited' else None), item
        runtime = SimpleNamespace(CHROMIUM_BINARY=h.CHROMIUM_BINARY, CHROMEDRIVER_BINARY=h.CHROMEDRIVER_BINARY,
                                  browser_preflight=lambda: None, port_free=lambda _: True, record=lambda *_: None,
                                  start=fake_start)
        def transport(_h, method, path, body=None, timeout=5):
            if method == 'GET':
                return {'ready': True}
            if method == 'POST':
                if case == 'create-timeout':
                    raise Failure('timeout')
                if case == 'http-error':
                    return decode(500, b'{"value":{"error":"session not created","message":"SECRET"}}')
                return {'sessionId': 'synthetic'}
            return None
        def cleaned(*_):
            cleanup_calls.append(True)
            return dict(complete_cleanup)
        with patch.dict(globals(), identity=lambda: ('1-1', folder), harness=lambda: runtime,
                        private_directory=lambda _: None, request=transport, cleanup=cleaned, verify_listeners=lambda *_: None, launch_driver=fake_start), \
             patch.object(Path, 'exists', lambda _: False), patch.object(Path, 'is_symlink', lambda _: False), \
             patch.object(Path, 'mkdir', lambda *a, **kw: None), patch.object(signal, 'signal', lambda *a: None), \
             patch.object(signal, 'alarm', lambda *a: None), redirect_stdout(StringIO()) as output:
            code = execute('run')
        require(launch_calls == [[h.CHROMEDRIVER_BINARY, '--port=9515', '--log-level=OFF']])
        require(not any(arg.startswith(('--allowed-ips', '--whitelisted-ips')) for arg in launch_calls[0]))
        result = json.loads(output.getvalue())
        require(code == (0 if case == 'success' else 1) and cleanup_calls == [True]
                and result['cleanup']['privateFilesGone'] and 'SECRET' not in output.getvalue())
        if case == 'create-timeout':
            require(result['checkpoint'] == 'session-create' and result['code'] == 'timeout')
        if case == 'driver-exited':
            require(result['code'] == 'driver-exited')
    # Fault-inject parent-only handler deferral; child signal masks never change.
    # No child, descriptor, file, or real signal is created.
    for fault in ('none', 'record', 'identity', 'stop', 'deferred', 'restore-term'):
        marker = [False]
        dead = [False]
        reaped = []
        original_handlers = {signal.SIGTERM: object(), signal.SIGALRM: object()}
        handlers = dict(original_handlers)
        group_stops = []
        process = SimpleNamespace(pid=123, poll=lambda: 0 if dead[0] else None,
                                  terminate=lambda: dead.__setitem__(0, True), kill=lambda: dead.__setitem__(0, True),
                                  wait=lambda **kw: reaped.append(True))
        def stop_registered(value):
            group_stops.append(value)
            if fault == 'stop':
                raise RuntimeError('synthetic stop failure')
            dead[0] = True
        def record_registered(*_):
            if fault == 'deferred':
                handlers[signal.SIGTERM](signal.SIGTERM, None)
            if fault in ('record', 'stop'):
                raise OSError('synthetic record failure')
        local = SimpleNamespace(CHROMEDRIVER_BINARY=h.CHROMEDRIVER_BINARY, private=lambda *_: marker.__setitem__(0, True),
                                runtime_env=lambda: {}, proc_identity=lambda _: None if fault == 'identity' else item,
                                record=record_registered, stop_process=stop_registered, group_members=lambda _: [])
        def handler(signum, value):
            old = handlers[signum]
            handlers[signum] = value
            if fault == 'restore-term' and signum == signal.SIGTERM and value is original_handlers[signum]:
                raise Failure('deadline')
            return old
        def popen(*args, **kw):
            require(marker[0] and all(handlers[key] is not original_handlers[key] for key in handlers) and kw['start_new_session'])
            require('preexec_fn' not in kw)
            require(args[0] == [h.CHROMEDRIVER_BINARY, '--port=9515', '--log-level=OFF'])
            return process
        with patch.object(subprocess, 'Popen', popen), patch.object(os, 'open', lambda *a: 123), \
             patch.object(os, 'close', lambda _: None), patch.object(signal, 'getsignal', lambda key: handlers[key]), \
             patch.object(signal, 'signal', handler), \
             patch.object(signal, 'pthread_sigmask', lambda *a: (_ for _ in ()).throw(AssertionError('signal mask modified'))), \
             patch.object(Path, 'unlink', lambda *a, **kw: marker.__setitem__(0, False)):
            try:
                registered = {'token': '1-1', 'processes': []}
                launch_driver(local, folder, registered)
                require(fault == 'none')
            except Failure as error:
                require(fault != 'none' and error.code == ('deadline' if fault in ('deferred', 'restore-term') else 'registration-failed' if fault == 'record' else 'registration-unconfirmed'))
        require(handlers == original_handlers)
        if fault in ('deferred', 'restore-term'):
            require(not marker[0] and registered['processes'] == [item] and not group_stops)
        if fault == 'record':
            require(dead[0] and reaped and group_stops == [item] and not marker[0])
        if fault == 'identity':
            require(dead[0] and reaped and not group_stops and marker[0])
        if fault in ('identity', 'stop'):
            # Persistent barrier prevents even a later cleanup from treating an
            # empty process list as permission to delete an uncertain profile.
            with patch.object(Path, 'exists', lambda _: True), patch.object(Path, 'is_symlink', lambda _: False), \
                 patch.dict(globals(), private_directory=lambda _: None, read_state=lambda *_: (_ for _ in ()).throw(AssertionError('state read past barrier'))):
                try:
                    cleanup(local, '1-1', folder)
                    raise AssertionError('uncertain registration cleanup passed')
                except Failure:
                    pass
    def row(address, inode='1234'):
        return ['header', '0: ' + address + ':' + format(9515, '04X') + ' ignored 0A ignored ignored ignored ignored ignored ' + inode]
    require(listener_rows(row('0100007F'), False, {'1234'}) == 1)
    require(listener_rows(row('00000000000000000000000001000000'), True, {'1234'}) == 1)
    for lines, ipv6, inodes in [(row('00000000'), False, {'1234'}), (row('0' * 32), True, {'1234'}),
                                 (row('0100007F'), False, {'foreign'})]:
        try:
            listener_rows(lines, ipv6, inodes)
            raise AssertionError('broad or foreign listener accepted')
        except Failure:
            pass
    options = capabilities(h, folder)['capabilities']['alwaysMatch']['goog:chromeOptions']
    require(options['browserStartupTimeout'] == 20000 and '--no-sandbox' not in options['args'])
    print('{"status":"PASS","checkpoint":"pure-selftest"}')


if __name__ == '__main__':
    os.umask(0o077)
    try:
        require(len(sys.argv) == 2 and sys.argv[1] in ('run', 'cleanup', '--selftest'))
        if sys.argv[1] == '--selftest':
            selftest()
        else:
            raise SystemExit(execute(sys.argv[1]))
    except Exception:
        print('{"status":"FAIL","checkpoint":"arguments-or-selftest"}')
        raise SystemExit(1)
