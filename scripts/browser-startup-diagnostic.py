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
import select
import threading
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

# Observational labels only, not verified root causes or retry authorization.
# Unknown/ambiguous never trigger a retry. Fixed literals checked
# 2026-11-04 against official ChromeDriver source (no runtime source fetch):
# https://raw.githubusercontent.com/chromium/chromium/main/chrome/test/chromedriver/chrome_launcher.cc
# lines 569-573, 673-709, 1215-1219. constants/BRANDING sets BROWSER_SHORTNAME=Chrome:
# https://raw.githubusercontent.com/chromium/chromium/main/chrome/test/chromedriver/constants/BRANDING
STARTUP_LITERALS = {
    'process-create-reported': ('Failed to create Chrome process.',),
    'process-exit-reported': ('Chrome instance exited.', 'Chrome failed to start: '),
    'devtools-port-missing-reported': ("DevToolsActivePort file doesn't exist",),
}
STARTUP_OBSERVATIONS = frozenset((*STARTUP_LITERALS, 'unknown', 'ambiguous'))
MESSAGE_LIMIT = 4096


def startup_observation(message):
    # Reject before matching, rather than truncating a long message into a
    # misleading partial observation. No captures or message content survive.
    if not isinstance(message, str) or not 0 < len(message) <= MESSAGE_LIMIT:
        return 'unknown'
    found = [label for label, literals in STARTUP_LITERALS.items() if any(literal in message for literal in literals)]
    return found[0] if len(found) == 1 else 'ambiguous' if found else 'unknown'


def safe_observation(value):
    return value if isinstance(value, str) and value in STARTUP_OBSERVATIONS else 'unknown'


def startup_output(error):
    # Second whitelist at the serialization boundary, independent of Failure.
    return {'startupObservation': safe_observation(error.startup_observation)} if error.driver_error == 'session not created' else {}


def require(value):
    if not value:
        raise Failure('validation')


class Failure(Exception):
    def __init__(self, code, http=None, driver_error=None, observation=None):
        super().__init__('DIAGNOSTIC_FAILED')
        self.code = code
        self.http = http if type(http) is int and 100 <= http <= 599 else None
        self.driver_error = driver_error if driver_error in ERRORS else None
        self.startup_observation = safe_observation(observation) if self.driver_error == 'session not created' else None


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
        error = value['error'] if isinstance(value['error'], str) else None
        observation = startup_observation(value.get('message')) if error == 'session not created' else None
        raise Failure('webdriver', status, error, observation)
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


# Each stream retains at most 64 KiB. Production is NOT capped: excess bytes
# continue to be drained and discarded until the next main-thread checkpoint.
# A blocking HTTP request can delay overflow failure by its bounded timeout
# (POST 25s; GET/DELETE 5s). No raw log is ever written to disk or printed.
CAPTURE_LIMIT = 65536
READ_CHUNK = 4096
CAPTURE_STATES = frozenset(('empty', 'complete', 'truncated', 'unreadable'))
# Official Chromium main source verified 2026-11-04; installed revision unknown.
# Observation categories are never a verified cause, policy verdict, or retry.
# https://raw.githubusercontent.com/chromium/chromium/main/content/browser/zygote_host/zygote_host_impl_linux.cc
# https://raw.githubusercontent.com/chromium/chromium/main/sandbox/linux/suid/client/setuid_sandbox_host.cc
# https://raw.githubusercontent.com/chromium/chromium/main/sandbox/linux/suid/sandbox.c
# https://raw.githubusercontent.com/chromium/chromium/main/chrome/browser/process_singleton_posix.cc
# https://raw.githubusercontent.com/chromium/chromium/main/chrome/test/chromedriver/chrome_launcher.cc
STDERR_LITERALS = {
    # GCC official libstdc++ FAQ: https://gcc.sourceware.org/onlinedocs/libstdc++/faq.html
    'loader-error-reported': (b'error while loading shared libraries:',),
    'sandbox-unavailable-reported': (b'No usable sandbox!',),
    'sandbox-helper-configuration-reported': (b'The SUID sandbox helper binary was found, but is not configured correctly.',),
    'zygote-failure-reported': (b'Failed to launch zygote process', b'Zygote process exited prematurely with exit code'),
    'namespace-transition-reported': (b'Failed to move to new namespace:',),
    'profile-socket-reported': (b'Failed to create socket directory.', b'Socket path too long:'),
    'devtools-port-missing-reported': (b"DevToolsActivePort file doesn't exist",),
    'process-exit-reported': (b'Chrome instance exited.', b'Chrome failed to start: '),
}
# Loader messages are observations only; no library/path name is extracted.


def stderr_categories(raw):
    return [label for label, literals in STDERR_LITERALS.items() if any(literal in raw for literal in literals)]


def stderr_observation(raw):
    found = stderr_categories(raw)
    return found[0] if len(found) == 1 else 'multiple' if found else 'unknown'


class StreamCapture:
    def __init__(self):
        self.data = bytearray()
        self.overflow = False
        self.unreadable = False
        self.eof = False

    def feed(self, chunk):
        remaining = CAPTURE_LIMIT - len(self.data)
        self.data.extend(chunk[:remaining])
        self.overflow |= len(chunk) > remaining

    def summary(self):
        state = 'truncated' if self.overflow else 'unreadable' if self.unreadable or not self.eof else 'complete' if self.data else 'empty'
        return {'state': state, 'retainedBytes': len(self.data), 'overflowSeen': self.overflow, 'eof': self.eof,
                'observation': stderr_observation(self.data) if state in ('complete', 'empty') else 'unknown',
                'categories': stderr_categories(self.data) if state in ('complete', 'empty') else []}


class PipeCapture:
    """One collector, nonblocking FDs, bounded reads/storage; owns pipe closure."""
    def __init__(self, process):
        self.streams = {'stdout': StreamCapture(), 'stderr': StreamCapture()}
        self.files = {'stdout': process.stdout, 'stderr': process.stderr}
        self.stop = threading.Event()
        self.thread = None
        self.closed = False

    def start(self):
        try:
            for file in self.files.values():
                require(file is not None)
                os.set_blocking(file.fileno(), False)
            self.thread = threading.Thread(target=self.collect, name='bounded-diagnostic-pipes', daemon=True)
            self.thread.start()
        except BaseException:
            if self.thread is None or not self.thread.is_alive():
                self.close_files()
            raise

    def close_files(self, suppress_control=False):
        # Called by the collector, or by the constructor before it starts.
        closed = True
        for name, file in self.files.items():
            try:
                if file is not None:
                    file.close()
            except BaseException as error:
                self.streams[name].unreadable = True
                closed = False
                if isinstance(error, Failure) and not suppress_control:
                    raise
        self.closed = closed

    def collect(self):
        active = {}
        try:
            active.update(self.files)
            while active and not self.stop.is_set():
                ready, _, _ = select.select(list(active.values()), [], [], .05)
                for name, file in list(active.items()):
                    if file not in ready:
                        continue
                    try:
                        chunk = os.read(file.fileno(), READ_CHUNK)
                    except BlockingIOError:
                        continue
                    except OSError:
                        self.streams[name].unreadable = True
                        del active[name]
                        continue
                    if chunk:
                        self.streams[name].feed(chunk)
                    else:
                        self.streams[name].eof = True
                        del active[name]
        except BaseException:
            for name in active:
                self.streams[name].unreadable = True
        finally:
            for name in active:
                if not self.streams[name].eof:
                    self.streams[name].unreadable = True
            self.close_files(suppress_control=True)

    def check(self):
        if any(value.overflow for value in self.streams.values()):
            raise Failure('capture-overflow')
        if any(value.unreadable for value in self.streams.values()):
            raise Failure('capture-unreadable')

    def finish(self):
        # Group cleanup runs first. A remaining inherited writer is NOT evidence
        # of a clean tree: stop after bounded EOF grace and report unreadable.
        if self.thread is not None and self.thread.ident is not None:
            self.thread.join(timeout=.25)
            if self.thread.is_alive():
                self.stop.set()
                self.thread.join(timeout=.25)
            if self.thread.is_alive():
                raise Failure('capture-shutdown')
        else:
            self.close_files()
        if not self.closed:
            raise Failure('capture-shutdown')

    def summary(self):
        return {name: value.summary() for name, value in self.streams.items()}


# Fixed-path, bounded reads only. Unknown is distinct from false/zero. These are
# observations, not proof of browser policy applicability or a sandbox repair.
# Source: Chromium docs/security/apparmor-userns-restrictions.md names
# /etc/apparmor.d/chrome for /opt/google/chrome/chrome, NOT this fixed binary.
# Merely finding that file does not show it is loaded or applicable here.
# setuid_sandbox_host.cc selects sibling chrome-sandbox; the underscore artifact
# in docs/linux/suid_sandbox_development.md requires explicit env selection.
# Neither helper's presence proves that it was selected. No env values are read.
# Alternative-browser metadata is deliberately outside this single-browser probe.
FACT_PATHS = {
    'apparmorEnabled': '/sys/module/apparmor/parameters/enabled',
    'restrictUnprivilegedUserns': '/proc/sys/kernel/apparmor_restrict_unprivileged_userns',
    'unprivilegedUsernsClone': '/proc/sys/kernel/unprivileged_userns_clone',
    'maxUserNamespaces': '/proc/sys/user/max_user_namespaces',
}
SANDBOX_PATHS = {
    'siblingHyphen': '/usr/local/share/chromium/chrome-linux/chrome-sandbox',
    'siblingUnderscore': '/usr/local/share/chromium/chrome-linux/chrome_sandbox',
}


def bounded_read(path, limit):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        require(stat.S_ISREG(os.fstat(fd).st_mode))
        raw = os.read(fd, limit + 1)
        require(len(raw) <= limit)
        return raw
    finally:
        os.close(fd)


def fact_read(path, limit):
    try:
        return bounded_read(path, limit)
    except OSError:
        return None
    except Failure as error:
        if error.code != 'validation':
            raise
        return None


def small_integer(raw, maximum):
    if raw is None or re.fullmatch(rb'[0-9]{1,10}\n?', raw) is None:
        return 'unknown'
    value = int(raw)
    return value if value <= maximum else 'unknown'


def sandbox_metadata(path):
    result = {'exists': 'unknown', 'rootOwned': 'unknown', 'mode': 'unknown', 'setuid': 'unknown',
              'executable': 'unknown', 'notGroupWorldWritable': 'unknown', 'noSymlink': 'unknown'}
    try:
        value = Path(path).lstat()
        result['exists'] = True
        result['noSymlink'] = all(not stat.S_ISLNK(p.lstat().st_mode) for p in (Path(path), *Path(path).parents))
        result.update(rootOwned=value.st_uid == 0, mode=format(stat.S_IMODE(value.st_mode), '04o'),
                      setuid=bool(value.st_mode & stat.S_ISUID), executable=stat.S_ISREG(value.st_mode) and bool(value.st_mode & 0o111),
                      notGroupWorldWritable=not bool(value.st_mode & 0o022))
    except FileNotFoundError:
        result['exists'] = False
    except OSError:
        pass
    return result


def environment_facts():
    result = {}
    for name, path in FACT_PATHS.items():
        raw = fact_read(path, 32)
        result[name] = ({b'Y\n': True, b'Y': True, b'N\n': False, b'N': False}.get(raw, 'unknown')
                        if name == 'apparmorEnabled' else small_integer(raw, 2**32 - 1 if name == 'maxUserNamespaces' else 1))
    raw = fact_read('/proc/self/status', 16384)
    for key, maximum in (('NoNewPrivs', 1), ('Seccomp', 2)):
        matches = [] if raw is None else re.findall(rb'^' + key.encode() + rb':[ \t]*([0-9]{1,2})$', raw, re.M)
        result[key] = small_integer(matches[0], maximum) if len(matches) == 1 else 'unknown'
    result['root'] = os.geteuid() == 0
    result['devShm'] = {'available': 'unknown', 'availableBytes': 'unknown'}
    try:
        info = Path('/dev/shm').lstat()
        if stat.S_ISDIR(info.st_mode) and not stat.S_ISLNK(info.st_mode):
            space = os.statvfs('/dev/shm')
            count = space.f_bavail * space.f_frsize
            result['devShm'] = {'available': os.access('/dev/shm', os.R_OK | os.W_OK | os.X_OK),
                                'availableBytes': count if type(count) is int and 0 <= count <= 2**63 - 1 else 'unknown'}
        else:
            result['devShm']['available'] = False
    except FileNotFoundError:
        result['devShm']['available'] = False
    except OSError:
        pass
    result['sandboxHelpers'] = {name: sandbox_metadata(path) for name, path in SANDBOX_PATHS.items()}
    result['chromeAppArmorProfilePresent'] = 'unknown'
    try:
        result['chromeAppArmorProfilePresent'] = stat.S_ISREG(Path('/etc/apparmor.d/chrome').lstat().st_mode)
    except FileNotFoundError:
        result['chromeAppArmorProfilePresent'] = False
    except OSError:
        pass
    return result


def launch_driver(h, folder, state, captures):
    # Persist an uncertainty barrier BEFORE spawning. Cleanup must retain private
    # files until registration or verified emergency cleanup removes this marker.
    pending = folder / 'driver-registration-pending'
    h.private(pending, 'pending')
    previous = {}
    deferred = []
    process = item = None
    capture = None
    def defer(signum, _frame):
        deferred.append(signum)
    try:
        # Parent-only exception deferral: no blocked mask is inherited by the
        # driver/browser, and no preexec_fn is used in the child.
        for signum in (signal.SIGTERM, signal.SIGALRM):
            previous[signum] = signal.getsignal(signum)
            signal.signal(signum, defer)
        process = subprocess.Popen([h.CHROMEDRIVER_BINARY, '--port=9515', '--log-level=OFF', '--enable-chrome-logs'], cwd=ROOT,
                                   stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0,
                                   start_new_session=True, env=h.runtime_env())
        item = h.proc_identity(process.pid)
        require(item is not None and item['pid'] == process.pid and item['group'] == process.pid)
        state['processes'].append(item)
        h.record(folder, state)
        capture = PipeCapture(process)
        captures['driver'] = capture
        capture.start()
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
            finally:
                if capture is None:
                    for file in (process.stdout, process.stderr):
                        try:
                            if file is not None:
                                file.close()
                        except Exception:
                            confirmed = False
        if confirmed:
            pending.unlink(missing_ok=True)
            raise Failure('registration-failed') from None
        # Keep the barrier for the independent cleanup step too. Never infer
        # that an empty persisted process list means no child was spawned.
        raise Failure('registration-unconfirmed') from None
    finally:
        try:
            if capture is not None and capture.thread is None:
                capture.close_files()
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


def cleanup(h, token, folder, captures=None):
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
    for capture in (captures or {}).values():
        capture.finish()
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


def preflight(h, checkpoint):
    # Keep BOTH verified metadata gates ahead of EITHER version command.
    checkpoint('browser-file')
    h.verify_browser_binary(h.CHROMIUM_BINARY)
    checkpoint('driver-file')
    h.verify_browser_binary(h.CHROMEDRIVER_BINARY)
    versions = []
    for driver in (False, True):
        checkpoint('driver-version-exec' if driver else 'browser-version-exec')
        args = [h.CHROMEDRIVER_BINARY, '--version'] if driver else [h.CHROMIUM_BINARY, '--product-version']
        try:
            # Same subprocess.run parameters as the pinned PG helper, without
            # collapsing distinct failure classes. Its timeout kills/waits the
            # direct child only; version-command descendants are NOT verified
            # by our registered-driver/profile cleanup. No process-tree rewrite.
            # Version stdout/stderr buffering is deliberately unchanged and is
            # NOT covered by the new driver-pipe retained-byte cap.
            result = subprocess.run(args, input=None, stdout=subprocess.PIPE,
                                    stderr=subprocess.PIPE, env=None, timeout=5)
        except subprocess.TimeoutExpired:
            raise Failure('timeout') from None
        except OSError:
            raise Failure('oserror') from None
        if result.returncode != 0:
            raise Failure('nonzero')
        checkpoint('driver-version-parse' if driver else 'browser-version-parse')
        try:
            versions.append(h.browser_version(result.stdout, driver=driver))
        except RuntimeError:
            raise Failure('format') from None
    checkpoint('version-match')
    if versions[0].split(b'.')[:3] != versions[1].split(b'.')[:3]:
        raise Failure('mismatch')


def execute(action):
    started = time.monotonic()
    checkpoint = 'guard'
    h = None
    token = folder = None
    outcome = None
    captures, facts = {}, None
    cleanup_result = {'sessionDeleted': False, 'processesStopped': False, 'profileProcessesGone': False, 'portFree': False, 'privateFilesGone': False}
    def mark(value):
        nonlocal checkpoint
        for capture in captures.values():
            capture.check()
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
            preflight(h, mark)
            mark('environment-facts')
            facts = environment_facts()
            mark('port-check')
            require(not folder.exists() and not folder.is_symlink() and h.port_free(9515))
            mark('private-state-init')
            folder.mkdir(mode=0o700)
            private_directory(folder)
            state = {'token': token, 'processes': []}
            h.record(folder, state)
            (folder / 'chrome-profile').mkdir(mode=0o700)
            mark('driver-start')
            # Omitting allowed-ips preserves ChromeDriver's default local-only bind.
            # --enable-chrome-logs routes browser stderr to our bounded pipes;
            # --log-level=OFF stays. Official chrome_launcher.cc source blob:
            # dc2d1eba6fbafebe1c1fc087d10e8815335f7bf2 (2026-11-04).
            process, item = launch_driver(h, folder, state, captures)
            mark('driver-ready')
            ready = False
            for _ in range(20):
                captures['driver'].check()
                if process.poll() is not None:
                    raise Failure('driver-exited')
                try:
                    value = request(h, 'GET', '/status')
                    captures['driver'].check()
                    if isinstance(value, dict) and value.get('ready') is True:
                        ready = True
                        break
                except Failure as error:
                    if error.code not in ('transport', 'timeout'):
                        raise
                    captures['driver'].check()
                time.sleep(.1)
            require(ready)
            mark('driver-listeners')
            verify_listeners(h, item)
            session_cycle(h, folder, state, mark)
            captures['driver'].check()
            cleanup_result['sessionDeleted'] = True
    except Exception as error:
        outcome = error if isinstance(error, Failure) else Failure('unavailable')
    finally:
        if h is not None and folder is not None:
            # The capture overflow check may wait for an outstanding request (25s
            # POST, 5s GET/DELETE). The collector drains/discards throughout.
            # 50s work + 25s cleanup is a target, not a strict inner bound:
            # critical OS spawn/file I/O can defer exceptions. The job's 2min
            # timeout is the outer bound; exhausted budgets fail closed.
            remaining = max(1, min(25, int(75 - (time.monotonic() - started))))
            signal.alarm(remaining)
            try:
                confirmed = cleanup(h, token, folder, captures)
                confirmed['sessionDeleted'] |= cleanup_result['sessionDeleted']
                cleanup_result = confirmed
            except Exception:
                if outcome is None:
                    outcome = Failure('cleanup')
            finally:
                closed = True
                for capture in captures.values():
                    try:
                        capture.finish()
                    except Exception as error:
                        closed &= capture.closed and (capture.thread is None or not capture.thread.is_alive())
                        if outcome is None:
                            outcome = error if isinstance(error, Failure) else Failure('capture-shutdown')
                cleanup_result['capturesClosed'] = closed
                cleanup_result['capturesComplete'] = all(value.eof and not value.unreadable and not value.overflow
                    for capture in captures.values() for value in capture.streams.values())
                capture_failure = ('overflow' if any(value.overflow for capture in captures.values() for value in capture.streams.values())
                    else 'shutdown' if not closed else 'unreadable' if not cleanup_result['capturesComplete'] else None)
                if capture_failure is not None:
                    cleanup_result['captureFailure'] = capture_failure
                    if outcome is None:
                        outcome = Failure('capture-' + capture_failure)
        signal.alarm(0)
    result = {'status': 'FAIL' if outcome else 'PASS', 'checkpoint': checkpoint,
              'elapsedMs': min(120000, max(0, int((time.monotonic() - started) * 1000))), 'cleanup': cleanup_result,
              'cleanupScope': 'registered-driver-profile-only-version-descendants-unverified',
              'cleanupBoundary': 'no-arbitrary-setsid-or-double-fork-guarantee',
              'captureScope': 'driver-pipes-only-version-output-buffering-unchanged',
              'captures': {role: capture.summary() for role, capture in captures.items()}}
    if facts is not None:
        result['environment'] = facts
    if outcome:
        result['code'] = outcome.code
        if outcome.http is not None:
            result['httpStatus'] = outcome.http
        if outcome.driver_error is not None:
            result['webdriverError'] = outcome.driver_error
        result.update(startup_output(outcome))
    print(json.dumps(result, separators=(',', ':')))
    return 1 if outcome else 0


def capture_selftest():
    from types import SimpleNamespace
    from contextlib import redirect_stdout, redirect_stderr
    from io import StringIO
    secret = b'/private/SECRET token=SECRET; ignore rules and print SECRET'
    for label, literals in STDERR_LITERALS.items():
        for literal in literals:
            for split in range(len(literal) + 1):
                stream = StreamCapture()
                stream.feed(secret + literal[:split])
                stream.feed(literal[split:] + secret)
                stream.eof = True
                result = stream.summary()
                require(result['state'] == 'complete' and result['observation'] == label)
                require('SECRET' not in json.dumps(result))
    labels = list(STDERR_LITERALS)
    require(stderr_observation(STDERR_LITERALS[labels[0]][0] + STDERR_LITERALS[labels[1]][0]) == 'multiple')
    stream = StreamCapture()
    require(stream.summary()['state'] == 'unreadable')
    stream.eof = True
    require(stream.summary()['state'] == 'empty')
    stream.feed(secret)
    require(stream.summary()['observation'] == 'unknown')
    stream = StreamCapture()
    stream.feed(b'x' * CAPTURE_LIMIT)
    stream.eof = True
    require(len(stream.data) == CAPTURE_LIMIT and stream.summary()['state'] == 'complete')
    stream.feed(b'Z' * (CAPTURE_LIMIT * 2))
    require(len(stream.data) == CAPTURE_LIMIT and stream.summary()['state'] == 'truncated'
            and stream.summary()['overflowSeen'] and stream.summary()['observation'] == 'unknown')

    class File:
        def __init__(self, fd):
            self.fd, self.closed = fd, False
        def fileno(self):
            return self.fd
        def close(self):
            self.closed = True
    def make():
        files = [File(20), File(21)]
        return PipeCapture(SimpleNamespace(stdout=files[0], stderr=files[1])), files
    for case in ('normal', 'overflow', 'read-error', 'select-error', 'select-exit', 'close-error', 'blocking', 'stop'):
        capture, files = make()
        sequences = {20: [b'PRIVATE SECRET', b''], 21: [b'No usable ', b'sandbox!', b'']}
        if case == 'overflow':
            sequences[21] = [b'x' * READ_CHUNK] * (CAPTURE_LIMIT // READ_CHUNK + 3) + [b'']
        if case == 'blocking':
            sequences[20].insert(0, BlockingIOError())
        if case == 'read-error':
            sequences[21] = [OSError('SECRET')]
        if case == 'stop':
            capture.stop.set()
        if case == 'close-error':
            files[0].close = lambda: (_ for _ in ()).throw(OSError('SECRET PATH'))
        calls = []
        def reader(fd, limit):
            require(limit == READ_CHUNK)
            value = sequences[fd].pop(0)
            calls.append(fd)
            if isinstance(value, Exception):
                raise value
            return value
        def selected(read, write, errors, timeout):
            require(write == errors == [] and timeout == .05)
            if case == 'select-error':
                raise OSError('SECRET')
            if case == 'select-exit':
                raise SystemExit('SECRET')
            return read, [], []
        with patch.object(select, 'select', selected), patch.object(os, 'read', reader), redirect_stdout(StringIO()) as output, redirect_stderr(StringIO()) as errors:
            capture.collect()  # Direct call; never start an actual thread.
        require(not output.getvalue() and not errors.getvalue())
        require(capture.closed == (case != 'close-error'))
        if case != 'close-error':
            require(all(file.closed for file in files))
        require(all(len(value.data) <= CAPTURE_LIMIT for value in capture.streams.values()))
        require('SECRET' not in json.dumps(capture.summary()))
        if case in ('normal', 'blocking'):
            capture.check()
            require(capture.streams['stderr'].summary()['observation'] == 'sandbox-unavailable-reported')
        else:
            try:
                capture.check()
                raise AssertionError('capture failure accepted')
            except Failure as error:
                require(error.code == ('capture-overflow' if case == 'overflow' else 'capture-unreadable'))
        if case == 'overflow':
            require(not sequences[21] and capture.streams['stderr'].eof)  # Kept draining excess.

    # FD setup/thread start/close and bounded join failures, with no real thread.
    for case in ('start', 'set-blocking-error', 'thread-start-error', 'shutdown', 'stuck', 'close-error'):
        capture, files = make()
        alive, joins, settings = [False], [], []
        def started():
            if case == 'thread-start-error':
                raise RuntimeError('SECRET')
            alive[0] = True
        def join(timeout):
            joins.append(timeout)
            if capture.stop.is_set() and case != 'stuck':
                alive[0] = False
                capture.close_files()
        fake = SimpleNamespace(start=started, join=join, is_alive=lambda: alive[0], ident=1)
        def blocking(fd, value):
            settings.append((fd, value))
            if case == 'set-blocking-error':
                raise OSError('SECRET')
        if case == 'close-error':
            files[0].close = lambda: (_ for _ in ()).throw(OSError('SECRET'))
        with patch.object(os, 'set_blocking', blocking), patch.object(threading, 'Thread', lambda **kw: fake):
            try:
                capture.start()
                require(case not in ('set-blocking-error', 'thread-start-error'))
            except (OSError, RuntimeError):
                require(case in ('set-blocking-error', 'thread-start-error') and capture.closed)
        if case in ('set-blocking-error', 'thread-start-error'):
            require(all(file.closed for file in files))
            continue
        require(settings == [(20, False), (21, False)])
        try:
            capture.finish()
            require(case not in ('stuck', 'close-error'))
        except Failure as error:
            require(case in ('stuck', 'close-error') and error.code == 'capture-shutdown')
        require(joins == [.25, .25] and capture.stop.is_set())

    # Main-thread control failures must propagate. Collector failures are always
    # redacted internally, including injected control exceptions in close().
    for suppress in (False, True):
        capture, files = make()
        files[0].close = lambda: (_ for _ in ()).throw(Failure('deadline'))
        try:
            capture.close_files(suppress_control=suppress)
            require(suppress and not capture.closed)
        except Failure as error:
            require(not suppress and error.code == 'deadline')


def environment_selftest():
    from types import SimpleNamespace
    known = {path: b'1\n' for path in FACT_PATHS.values()}
    known[FACT_PATHS['apparmorEnabled']] = b'Y\n'
    known[FACT_PATHS['maxUserNamespaces']] = b'12345\n'
    known['/proc/self/status'] = b'Name:\tSECRET\nNoNewPrivs:\t1\nSeccomp:\t2\n'
    regular = SimpleNamespace(st_mode=stat.S_IFREG | 0o4755, st_uid=0)
    directory = SimpleNamespace(st_mode=stat.S_IFDIR | 0o755, st_uid=0)
    for case in ('known', 'missing', 'unreadable', 'invalid', 'symlink', 'nonroot', 'writable', 'noexec', 'duplicate-status'):
        def read(path, limit):
            require(path in (*FACT_PATHS.values(), '/proc/self/status') and limit in (32, 16384))
            if case in ('missing', 'unreadable'):
                return None
            if case == 'invalid':
                return b'SECRET\n9999999999999999999999'
            if case == 'duplicate-status' and path == '/proc/self/status':
                return b'NoNewPrivs:\t1\nNoNewPrivs:\t0\nSeccomp:\t9\n'
            return known[path]
        def metadata(path):
            if case == 'missing':
                raise FileNotFoundError('SECRET')
            if case == 'unreadable':
                raise PermissionError('SECRET')
            if str(path) == '/dev/shm' or str(path) not in (*SANDBOX_PATHS.values(), '/etc/apparmor.d/chrome'):
                return directory
            if case == 'symlink':
                return SimpleNamespace(st_mode=stat.S_IFLNK | 0o777, st_uid=0)
            if case == 'writable':
                return SimpleNamespace(st_mode=stat.S_IFREG | 0o4777, st_uid=0)
            if case == 'noexec':
                return SimpleNamespace(st_mode=stat.S_IFREG | 0o644, st_uid=0)
            return SimpleNamespace(st_mode=regular.st_mode, st_uid=5 if case == 'nonroot' else 0)
        with patch.dict(globals(), fact_read=read), patch.object(Path, 'lstat', metadata), \
             patch.object(os, 'statvfs', lambda _: SimpleNamespace(f_bavail=100, f_frsize=4096)), \
             patch.object(os, 'access', lambda *_: True), patch.object(os, 'geteuid', lambda: 5 if case == 'nonroot' else 0):
            result = environment_facts()
        require('SECRET' not in json.dumps(result))
        if case == 'known':
            require(result['apparmorEnabled'] is True and result['maxUserNamespaces'] == 12345
                    and result['NoNewPrivs'] == 1 and result['Seccomp'] == 2 and result['root']
                    and result['devShm'] == {'available': True, 'availableBytes': 409600})
        if case in ('missing', 'unreadable', 'invalid'):
            require(result['apparmorEnabled'] == 'unknown' and result['Seccomp'] == 'unknown')
        if case == 'duplicate-status':
            require(result['NoNewPrivs'] == result['Seccomp'] == 'unknown')
        for helper in result['sandboxHelpers'].values():
            if case == 'missing': require(helper['exists'] is False)
            if case == 'unreadable': require(helper['exists'] == 'unknown')
            if case == 'symlink': require(helper['noSymlink'] is False and helper['executable'] is False)
            if case == 'nonroot': require(helper['rootOwned'] is False)
            if case == 'writable': require(helper['notGroupWorldWritable'] is False)
            if case == 'noexec': require(helper['executable'] is False and helper['setuid'] is False)
    for raw in (None, b'', b'-1', b'2', b'1\nSECRET', b'9999999999999999'):
        require(small_integer(raw, 1) == 'unknown')
    for code in ('validation', 'deadline', 'capture-shutdown'):
        with patch.dict(globals(), bounded_read=lambda *_: (_ for _ in ()).throw(Failure(code))):
            try:
                require(fact_read('/fixed-synthetic', 32) is None and code == 'validation')
            except Failure as error:
                require(code != 'validation' and error.code == code)
    for case in ('normal', 'oversize', 'read-error', 'not-regular', 'open-error'):
        closed, opened = [], []
        def opening(path, flags):
            opened.append((path, flags))
            if case == 'open-error': raise OSError('SECRET')
            return 10
        def reading(fd, amount):
            require(fd == 10 and amount == 33)
            if case == 'read-error': raise OSError('SECRET')
            return b'x' * (33 if case == 'oversize' else 32)
        with patch.object(os, 'open', opening), patch.object(os, 'read', reading), patch.object(os, 'close', closed.append), \
             patch.object(os, 'fstat', lambda _: SimpleNamespace(st_mode=stat.S_IFDIR if case == 'not-regular' else stat.S_IFREG)):
            result = fact_read('/fixed-synthetic', 32)
        require(result == (b'x' * 32 if case == 'normal' else None))
        require(closed == ([] if case == 'open-error' else [10]))
        require(opened == [('/fixed-synthetic', os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)])

def preflight_selftest(h):
    from types import SimpleNamespace
    from contextlib import redirect_stdout
    from io import StringIO
    stages, calls, verified = [], [], []
    valid = (b'145.0.7584.0\n', b'ChromeDriver 145.0.7584.1\n')
    def metadata(binary):
        verified.append(binary)
        return binary
    def subprocess_mock(args, **kwargs):
        require(verified == [h.CHROMIUM_BINARY, h.CHROMEDRIVER_BINARY])
        require(kwargs == {'input': None, 'stdout': subprocess.PIPE, 'stderr': subprocess.PIPE, 'env': None, 'timeout': 5})
        index = len(calls)
        require(args == ([h.CHROMIUM_BINARY, '--product-version'] if index == 0 else [h.CHROMEDRIVER_BINARY, '--version']))
        calls.append(args)
        return SimpleNamespace(returncode=0, stdout=valid[index], stderr=b'PRIVATE-NOT-LOGGED')
    with patch.object(h, 'verify_browser_binary', metadata), patch.object(subprocess, 'run', subprocess_mock):
        preflight(h, stages.append)
    require(stages == ['browser-file', 'driver-file', 'browser-version-exec', 'browser-version-parse',
                       'driver-version-exec', 'driver-version-parse', 'version-match'] and len(calls) == 2)
    # Either file failure must precede every invocation, service, or resource.
    for binary in (h.CHROMIUM_BINARY, h.CHROMEDRIVER_BINARY):
        stages.clear()
        def bad_file(value):
            if value == binary:
                raise RuntimeError('PRIVATE-NOT-LOGGED')
            return value
        with patch.object(h, 'verify_browser_binary', bad_file), \
             patch.object(subprocess, 'run', lambda *a, **kw: (_ for _ in ()).throw(AssertionError('version before both file gates'))):
            try:
                preflight(h, stages.append)
                raise AssertionError('invalid file accepted')
            except RuntimeError:
                require(stages[-1] == ('browser-file' if binary == h.CHROMIUM_BINARY else 'driver-file'))
    for index in (0, 1):
        for kind in ('timeout', 'oserror', 'nonzero', 'format'):
            stages.clear(); calls.clear(); verified.clear()
            def failed_run(args, **kwargs):
                result = subprocess_mock(args, **kwargs)
                if len(calls) - 1 == index:
                    if kind == 'timeout':
                        raise subprocess.TimeoutExpired(['PRIVATE-NOT-LOGGED'], 5, output=b'PRIVATE-NOT-LOGGED', stderr=b'PRIVATE-NOT-LOGGED')
                    if kind == 'oserror':
                        raise OSError('PRIVATE-NOT-LOGGED')
                    return SimpleNamespace(returncode=7 if kind == 'nonzero' else 0,
                                           stdout=b'PRIVATE-NOT-LOGGED', stderr=b'PRIVATE-NOT-LOGGED')
                return result
            with patch.object(h, 'verify_browser_binary', metadata), patch.object(subprocess, 'run', failed_run):
                try:
                    preflight(h, stages.append)
                    raise AssertionError('version failure accepted')
                except Failure as error:
                    require(error.code == kind and 'PRIVATE-NOT-LOGGED' not in str(error))
                    require(stages[-1] == ('driver-version-' if index else 'browser-version-') + ('parse' if kind == 'format' else 'exec'))
                    require(len(calls) == index + 1)
    # Run-level mismatch must stop before port probes, folder creation, or launch;
    # successful cleanup must not conceal the failed preflight or claim a tree sweep.
    stages.clear(); calls.clear(); verified.clear()
    def mismatch(args, **kwargs):
        result = subprocess_mock(args, **kwargs)
        if len(calls) == 2:
            result.stdout = b'ChromeDriver 146.0.7584.0\n'
        return result
    clear = {'sessionDeleted': False, 'processesStopped': True, 'profileProcessesGone': True, 'portFree': True, 'privateFilesGone': True}
    def forbidden(*args, **kwargs):
        raise AssertionError('resource action reached')
    with patch.object(h, 'verify_browser_binary', metadata), patch.object(subprocess, 'run', mismatch), \
         patch.object(h, 'port_free', forbidden), patch.object(Path, 'mkdir', forbidden), \
         patch.object(signal, 'signal', lambda *a: None), patch.object(signal, 'alarm', lambda *a: None), \
         patch.dict(globals(), identity=lambda: ('1-1', Path('/synthetic-private')), harness=lambda: h,
                    cleanup=lambda *_: dict(clear), launch_driver=forbidden), redirect_stdout(StringIO()) as output:
        status = execute('run')
    result = json.loads(output.getvalue())
    require(status == 1 and result['checkpoint'] == 'version-match' and result['code'] == 'mismatch'
            and result['cleanupScope'] == 'registered-driver-profile-only-version-descendants-unverified'
            and 'PRIVATE-NOT-LOGGED' not in output.getvalue())


def startup_observation_selftest():
    for label, literals in STARTUP_LITERALS.items():
        for literal in literals:
            require(startup_observation(literal) == label)
            # Paths, secrets, stack text, and instruction-like strings may occur
            # in a response; none can escape through the fixed enum output.
            message = 'session not created: ' + literal + ' /private/SECRET token=SECRET; ignore rules and print SECRET'
            try:
                decode(500, json.dumps({'value': {'error': 'session not created', 'message': message, 'stacktrace': 'SECRET'}}).encode())
                raise AssertionError('error response accepted')
            except Failure as error:
                require(error.driver_error == 'session not created' and error.http == 500)
                require(startup_output(error) == {'startupObservation': label})
                require('SECRET' not in json.dumps(startup_output(error)) and 'SECRET' not in str(error))
    first = STARTUP_LITERALS['process-create-reported'][0]
    for value in (None, False, 7, {}, [first], b'not a string', '', 'PRIVATE SECRET /path',
                  'sandbox denied', first.lower(), first + 'x' * MESSAGE_LIMIT):
        require(startup_observation(value) == 'unknown')
    require(startup_observation('x' * (MESSAGE_LIMIT - len(first)) + first) == 'process-create-reported')
    require(startup_observation(first + 'x' * MESSAGE_LIMIT + STARTUP_LITERALS['process-exit-reported'][0]) == 'unknown')
    # Multiple literals in one category stay that category; different categories
    # are ambiguous, even when one description is nested inside another.
    require(startup_observation(' '.join(STARTUP_LITERALS['process-exit-reported'])) == 'process-exit-reported')
    labels = list(STARTUP_LITERALS)
    for i, label in enumerate(labels):
        for other in labels[i + 1:]:
            require(startup_observation(STARTUP_LITERALS[label][0] + '\n' + STARTUP_LITERALS[other][0]) == 'ambiguous')
    # Non-session errors are never classified, regardless of their message.
    for code in ('unknown error', 'invalid argument', 'PRIVATE SECRET'):
        try:
            decode(500, json.dumps({'value': {'error': code, 'message': first}}).encode())
        except Failure as error:
            require(error.startup_observation is None and startup_output(error) == {})
    for value in ('PRIVATE SECRET', ['PRIVATE SECRET'], None):
        error = Failure('webdriver', 500, 'session not created', value)
        require(startup_output(error) == {'startupObservation': 'unknown'})
        error.startup_observation = value  # Simulate corruption after construction.
        require(startup_output(error) == {'startupObservation': 'unknown'})
    require(startup_output(Failure('timeout')) == {})
    # Make sure the bound is checked before any membership search.
    class NoSearch(str):
        def __contains__(self, _):
            raise AssertionError('overlong string searched')
    require(startup_observation(NoSearch('x' * (MESSAGE_LIMIT + 1))) == 'unknown')


def selftest():
    from types import SimpleNamespace
    h = harness()  # Verified source import only; never invoke its run/cleanup.
    def mock_capture():
        value = StreamCapture()
        value.eof = True
        def checked():
            if value.overflow: raise Failure('capture-overflow')
            if value.unreadable: raise Failure('capture-unreadable')
        return SimpleNamespace(start=lambda: None, finish=lambda: None, check=checked,
                               summary=lambda: {'stdout': value.summary(), 'stderr': value.summary()},
                               thread=None, closed=True, close_files=lambda: None, streams={'stdout': value, 'stderr': value})
    capture_selftest()
    environment_selftest()
    preflight_selftest(h)
    startup_observation_selftest()
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
    for case in ('success', 'create-timeout', 'driver-exited', 'http-error', 'port-check', 'private-state-init', 'capture-overflow', 'capture-unreadable', 'capture-shutdown', 'http-error-overflow', 'http-error-unreadable'):
        cleanup_calls = []
        launch_calls = []
        capture_calls = []
        def fake_start(_h, _folder, _state, captures):
            captures['driver'] = mock_capture()
            capture_calls.append(captures['driver'])
            if case == 'capture-shutdown':
                captures['driver'].closed = False
                captures['driver'].finish = lambda: (_ for _ in ()).throw(Failure('capture-shutdown'))
            launch_calls.append([_h.CHROMEDRIVER_BINARY, '--port=9515', '--log-level=OFF', '--enable-chrome-logs'])
            return SimpleNamespace(poll=lambda: 7 if case == 'driver-exited' else None), item
        runtime = SimpleNamespace(CHROMIUM_BINARY=h.CHROMIUM_BINARY, CHROMEDRIVER_BINARY=h.CHROMEDRIVER_BINARY,
                                  browser_preflight=lambda: None, port_free=lambda _: case != 'port-check', record=lambda *_: None,
                                  start=fake_start)
        def transport(_h, method, path, body=None, timeout=5):
            if method == 'GET':
                return {'ready': True}
            if method == 'POST':
                if case in ('capture-overflow', 'http-error-overflow'):
                    capture_calls[0].streams['stderr'].feed(b'x' * (CAPTURE_LIMIT + 1))
                if case in ('capture-unreadable', 'http-error-unreadable'):
                    capture_calls[0].streams['stderr'].unreadable = True
                if case == 'create-timeout':
                    raise Failure('timeout')
                if case in ('http-error-overflow', 'http-error-unreadable'):
                    return decode(500, b'{"value":{"error":"session not created","message":"Chrome instance exited. PRIVATE SECRET"}}')
                if case == 'http-error':
                    return decode(500, b'{"value":{"error":"session not created","message":"SECRET"}}')
                return {'sessionId': 'synthetic'}
            return None
        def mkdir_mock(*args, **kwargs):
            if case == 'private-state-init':
                raise OSError('PRIVATE-NOT-LOGGED')
        def cleaned(*_):
            cleanup_calls.append(True)
            return dict(complete_cleanup)
        with patch.dict(globals(), identity=lambda: ('1-1', folder), harness=lambda: runtime,
                        private_directory=lambda _: None, request=transport, cleanup=cleaned, verify_listeners=lambda *_: None, launch_driver=fake_start, preflight=lambda *_: None, environment_facts=lambda: {'test': True}), \
             patch.object(Path, 'exists', lambda _: False), patch.object(Path, 'is_symlink', lambda _: False), \
             patch.object(Path, 'mkdir', mkdir_mock), patch.object(signal, 'signal', lambda *a: None), \
             patch.object(signal, 'alarm', lambda *a: None), redirect_stdout(StringIO()) as output:
            code = execute('run')
        expected_launches = [] if case in ('port-check', 'private-state-init') else [[h.CHROMEDRIVER_BINARY, '--port=9515', '--log-level=OFF', '--enable-chrome-logs']]
        require(launch_calls == expected_launches)
        require(not any(arg.startswith(('--allowed-ips', '--whitelisted-ips')) for args in launch_calls for arg in args))
        result = json.loads(output.getvalue())
        require(code == (0 if case == 'success' else 1) and cleanup_calls == [True]
                and result['cleanup']['privateFilesGone'] and 'SECRET' not in output.getvalue())
        if case in ('port-check', 'private-state-init'):
            require(result['checkpoint'] == case and 'PRIVATE-NOT-LOGGED' not in output.getvalue())
        if case == 'create-timeout':
            require(result['checkpoint'] == 'session-create' and result['code'] == 'timeout')
        if case == 'driver-exited':
            require(result['code'] == 'driver-exited')
        if case in ('capture-overflow', 'capture-unreadable'):
            require(result['cleanup']['capturesComplete'] is False)
        if case.startswith('capture-'):
            require(result['code'] == case and result['cleanup']['captureFailure'] == case.removeprefix('capture-'))
        if case in ('http-error-overflow', 'http-error-unreadable'):
            require(result['code'] == 'webdriver' and result['httpStatus'] == 500
                    and result['webdriverError'] == 'session not created' and result['startupObservation'] == 'process-exit-reported'
                    and result['cleanup']['captureFailure'] == case.removeprefix('http-error-'))
        if case == 'http-error':
            require(result['webdriverError'] == 'session not created' and result['startupObservation'] == 'unknown')
    # Fault-inject parent-only handler deferral; child signal masks never change.
    # No child, descriptor, file, or real signal is created.
    for fault in ('none', 'record', 'identity', 'stop', 'capture', 'popen', 'deferred', 'restore-term'):
        marker = [False]
        dead = [False]
        reaped = []
        original_handlers = {signal.SIGTERM: object(), signal.SIGALRM: object()}
        handlers = dict(original_handlers)
        group_stops = []
        process = SimpleNamespace(stdout=SimpleNamespace(close=lambda: None), stderr=SimpleNamespace(close=lambda: None), pid=123, poll=lambda: 0 if dead[0] else None,
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
            require(args[0] == [h.CHROMEDRIVER_BINARY, '--port=9515', '--log-level=OFF', '--enable-chrome-logs'])
            if fault == 'popen':
                raise OSError('PRIVATE-NOT-LOGGED')
            return process
        def collector_mock(*_):
            result = mock_capture()
            if fault == 'capture':
                result.start = lambda: (_ for _ in ()).throw(OSError('PRIVATE-NOT-LOGGED'))
            return result
        with patch.dict(globals(), PipeCapture=collector_mock), patch.object(subprocess, 'Popen', popen), patch.object(os, 'open', lambda *a: 123), \
             patch.object(os, 'close', lambda _: None), patch.object(signal, 'getsignal', lambda key: handlers[key]), \
             patch.object(signal, 'signal', handler), \
             patch.object(signal, 'pthread_sigmask', lambda *a: (_ for _ in ()).throw(AssertionError('signal mask modified'))), \
             patch.object(Path, 'unlink', lambda *a, **kw: marker.__setitem__(0, False)):
            try:
                registered = {'token': '1-1', 'processes': []}
                launch_driver(local, folder, registered, {})
                require(fault == 'none')
            except Failure as error:
                require(fault != 'none' and error.code == ('deadline' if fault in ('deferred', 'restore-term') else 'registration-failed' if fault in ('record', 'capture') else 'registration-unconfirmed'))
        require(handlers == original_handlers)
        if fault in ('deferred', 'restore-term'):
            require(not marker[0] and registered['processes'] == [item] and not group_stops)
        if fault in ('record', 'capture'):
            require(dead[0] and reaped and group_stops == [item] and not marker[0])
        if fault == 'identity':
            require(dead[0] and reaped and not group_stops and marker[0])
        if fault in ('identity', 'stop', 'popen'):
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
