#!/usr/bin/env python3
"""Unexecuted owner-only authority PG candidate. --selftest uses in-memory fakes.

The run entry is deliberately opt-in, separate from existing automatic CI.
No production callbacks, credentials, role changes or runtime wiring exist here.
"""
import hashlib
import selectors
import signal
import json
import math
import os
from pathlib import Path
import re
import secrets
import shutil
import stat
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
PACKAGE = 'github.com/ZhangIvan/worktool-open/backend-go/internal/callback/durableauthority'
TEST_RE = '^TestPGAuthorityA(0[1-9]|1[0-6])$'
PARENTS = tuple(f'TestPGAuthorityA{i:02d}' for i in range(1, 17))
IMAGE = 'postgres@sha256:41a369edc91d9bdd7a58be6bc71ea0f8f7a3f49e82818a9f0829021ea7840b7c'
SCHEMA_SHA256 = 'f029a454b01705e2f02479f4280285f0aeeab57824c3d2d259f4bb27ef375294'
# Filled only after the independent test author freezes the exact leaf manifest.
MANIFEST_SHA256 = '23654ccf025d8ae5886fcd351b0f0db70d4501b902b0dddcdfb6e7032bfda7f3'
GO_VERSION = 'go1.27.3'
LABEL = 'io.worktool.authority.run'
MAX_RAW = 16 * 1024 * 1024
MAX_JSON = 32 * 1024 * 1024
MAX_LINE = 65536
HEX32 = re.compile(r'^[0-9a-f]{32}$')
HEX64 = re.compile(r'^[0-9a-f]{64}$')
SAFE_STATES = frozenset({'23502', '23503', '23505', '23514', '25P02', '42P06', '0A000', '55P03', '57014'})
SAFE_CODES = frozenset({
    'ARGUMENTS', 'EXECUTION_NOT_APPROVED', 'RUNTIME_UNAVAILABLE', 'GO_VERSION',
    'SOURCE_DRIFT', 'MANIFEST_INVALID', 'MANIFEST_DRIFT', 'TARGET_INVALID',
    'RESOURCE_MISMATCH', 'IMAGE_MISMATCH', 'IDENTITY_MISMATCH', 'PRIVATE_PATH',
    'RUN_ALREADY_EXISTS', 'STATE_INVALID', 'DOCKER_CONFIGURATION', 'TOOLCHAIN_HASH', 'PROCESS_FAILED', 'PROCESS_TIMEOUT',
    'PROCESS_OUTPUT_LIMIT', 'PROCESS_UNCERTAIN', 'READINESS_TIMEOUT',
    'TEST_PROCESS_FAILED', 'CONVERTER_FAILED', 'EVIDENCE_INVALID',
    'EVIDENCE_MISSING', 'TEST_FAILED_OR_SKIPPED', 'CLEANUP_UNCONFIRMED',
    'CLEANUP_IDENTITY_MISMATCH', 'UNEXPECTED_FAILURE', 'SELFTEST_FAILED',
})


class Failure(Exception):
    def __init__(self, code):
        super().__init__(code if code in SAFE_CODES else 'UNEXPECTED_FAILURE')
        self.code = str(self)


def require(ok, code):
    if not ok:
        raise Failure(code)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def strict_json(raw, limit):
    require(isinstance(raw, bytes) and len(raw) <= limit, 'EVIDENCE_INVALID')
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, 'EVIDENCE_INVALID')
            result[key] = value
        return result
    try:
        return json.loads(raw.decode('utf-8'), object_pairs_hook=unique,
                          parse_constant=lambda _: (_ for _ in ()).throw(Failure('EVIDENCE_INVALID')))
    except Failure:
        raise
    except (ValueError, UnicodeError):
        raise Failure('EVIDENCE_INVALID') from None


def manifest(raw, expected):
    require(bool(HEX64.fullmatch(expected)), 'MANIFEST_DRIFT')
    require(sha(raw) == expected, 'MANIFEST_DRIFT')
    value = strict_json(raw, 262144)
    require(isinstance(value, dict) and set(value) == {'version', 'package', 'tests'}, 'MANIFEST_INVALID')
    require(type(value['version']) is int and value['version'] == 1 and value['package'] == PACKAGE, 'MANIFEST_INVALID')
    tests = value['tests']
    require(isinstance(tests, list) and 32 <= len(tests) <= 1000, 'MANIFEST_INVALID')
    require(all(isinstance(t, str) and re.fullmatch(r'TestPGAuthorityA(?:0[1-9]|1[0-6])(?:/[A-Za-z0-9_]+)*', t) for t in tests), 'MANIFEST_INVALID')
    require(len(tests) == len(set(tests)) and set(PARENTS) <= set(tests), 'MANIFEST_INVALID')
    for parent in PARENTS:
        require(any(t.startswith(parent + '/') for t in tests), 'MANIFEST_INVALID')
    for name in tests:
        if '/' in name:
            require(name.rsplit('/', 1)[0] in tests, 'MANIFEST_INVALID')
    return tuple(tests)


def validate_target(v):
    expected_keys = {'version', 'run_nonce', 'database', 'user', 'password', 'host', 'port',
                     'schema_sha256', 'binary_sha256', 'image', 'network_mode', 'pid_mode',
                     'cpu_nanos', 'memory_bytes', 'memory_swap_bytes', 'shm_bytes',
                     'tmpfs_bytes', 'no_mounts_or_ports', 'run_owned'}
    require(isinstance(v, dict) and set(v) == expected_keys, 'TARGET_INVALID')
    nonce = v['run_nonce']
    require(isinstance(nonce, str) and HEX32.fullmatch(nonce) is not None, 'TARGET_INVALID')
    fixed = {'version': 1, 'database': 'wt_authority_test_' + nonce,
             'user': 'wt_authority_owner_' + nonce, 'host': '127.0.0.1', 'port': 5432,
             'schema_sha256': SCHEMA_SHA256, 'image': IMAGE, 'network_mode': 'none',
             'pid_mode': '', 'cpu_nanos': 2000000000, 'memory_bytes': 1073741824,
             'memory_swap_bytes': 1073741824, 'shm_bytes': 67108864,
             'tmpfs_bytes': 2147483648, 'no_mounts_or_ports': True, 'run_owned': True}
    require(all(type(v[k]) is type(x) and v[k] == x for k, x in fixed.items()), 'TARGET_INVALID')
    require(all(isinstance(v[k], str) and HEX64.fullmatch(v[k]) for k in ('password', 'binary_sha256')), 'TARGET_INVALID')
    require(len(json.dumps(v).encode()) <= 8192, 'TARGET_INVALID')
    return v


# Only allowlisted fields are requested. Never request Config.Env or full inspect.
INSPECT_FIELDS = {
    'Id': '.Id', 'Name': '.Name', 'Image': '.Image', 'ConfigImage': '.Config.Image',
    'Labels': '.Config.Labels', 'Mounts': '.Mounts', 'Ports': '.NetworkSettings.Ports',
    'Running': '.State.Running',
    **{k: '.HostConfig.' + k for k in (
        'NetworkMode', 'PidMode', 'IpcMode', 'UsernsMode', 'UTSMode', 'CgroupnsMode',
        'Privileged', 'ReadonlyRootfs', 'Binds', 'VolumesFrom', 'Devices',
        'DeviceRequests', 'DeviceCgroupRules', 'CapAdd', 'CapDrop', 'SecurityOpt', 'Memory', 'MemorySwap',
        'NanoCpus', 'ShmSize', 'Tmpfs', 'PortBindings', 'PublishAllPorts',
        'Runtime', 'RestartPolicy', 'AutoRemove', 'LogConfig', 'Sysctls', 'CgroupParent',
        'VolumeDriver', 'StorageOpt', 'GroupAdd', 'Links', 'ExtraHosts', 'Dns',
        'DnsOptions', 'DnsSearch', 'OomKillDisable')},
}
INSPECT_TEMPLATE = '{' + ','.join('"' + k + '":{{json ' + v + '}}' for k,v in INSPECT_FIELDS.items()) + '}'
TMPFS = {'/var/lib/postgresql': 'rw,nosuid,nodev,size=2147483648'}


# Diagnostic observations never participate in acceptance or authorize execution.
DIAGNOSTIC_PHASES = frozenset({
    'WORK', 'BUILD', 'IMAGE_CHECK', 'CREATE', 'RESOURCE_INSPECT', 'READINESS',
    'RESOURCE_RECHECK', 'PATH_CHECK', 'COPY_BINARY', 'TEST_EXECUTION',
    'EVIDENCE_VALIDATION', 'TOOLCHAIN_RECHECK', 'CLEANUP', 'CLEANUP_PREFLIGHT',
    'CLEANUP_STATE', 'CLEANUP_LIST', 'CLEANUP_INSPECT', 'CLEANUP_REMOVE',
    'CLEANUP_ABSENCE', 'CLEANUP_STATE_REMOVE',
})


def inspect_failure(code, raw):
    """Conservative hints, not root-cause proof; never retain arbitrary bytes."""
    result = {'exit_code': code if type(code) is int and -127 <= code <= 255 else None,
              'category': 'UNKNOWN', 'field': None}
    if type(raw) is not bytes or len(raw) > 4096:
        return result
    try:
        text = raw.decode('ascii')
    except UnicodeError:
        return result
    # Only exact frozen projection labels can leave this classifier. A match
    # says what the client reported, not whether that was the underlying cause.
    for label, path in INSPECT_FIELDS.items():
        token = path.rsplit('.', 1)[-1]
        if ('executing at <' + path + '>') in text and ('map has no entry for key "' + token + '"') in text:
            result.update(category='MISSING_FIELD_REPORTED', field=label)
            return result
    if text.startswith(('Error: No such object:', 'Error response from daemon: No such container:')):
        result['category'] = 'OBJECT_ABSENCE_REPORTED'
    return result


class Diagnostics:
    """At most 64 events and one <=16 KiB record, after cleanup only.

    provenance denotes entrypoint, not proof that SQL or cleanup succeeded.
    """
    def __init__(self, provenance):
        self.provenance = provenance if type(provenance) is str and provenance in ('RUN', 'RECOVERY', 'SYNTHETIC') else 'SYNTHETIC'
        self.events = []
        self.outcomes = {side: {'phase': side.upper(), 'status': 'NOT_STARTED', 'code': None,
                                'inspect': None} for side in ('work', 'cleanup')}
        self.emitted = False

    def phase(self, side, phase):
        if type(side) is not str or type(phase) is not str or side not in self.outcomes or phase not in DIAGNOSTIC_PHASES:
            return
        current = self.outcomes[side]
        if current['status'] == 'RUNNING' and len(self.events) < 64:
            self.events.append({'side': side, 'phase': current['phase'], 'event': 'COMPLETED'})
        current.update(phase=phase, status='RUNNING')
        if len(self.events) < 64:
            self.events.append({'side': side, 'phase': phase, 'event': 'STARTED'})

    def finish(self, side, exc=None):
        if type(side) is not str or side not in self.outcomes:
            return
        current = self.outcomes[side]
        current['status'] = 'SUCCEEDED' if exc is None else 'FAILED'
        current['code'] = None if exc is None else (exc.code if type(exc) is Failure and type(exc.code) is str and exc.code in SAFE_CODES else 'UNEXPECTED_FAILURE')
        if type(exc) is Failure:
            detail = getattr(exc, 'inspect_diagnostic', None)
            if type(detail) is dict and all(type(key) is str for key in detail) and set(detail) == {'exit_code', 'category', 'field'}:
                code, category, field = detail['exit_code'], detail['category'], detail['field']
                if (code is None or type(code) is int and -127 <= code <= 255) and type(category) is str and category in (
                        'UNKNOWN', 'MISSING_FIELD_REPORTED', 'OBJECT_ABSENCE_REPORTED') and (field is None or type(field) is str and field in INSPECT_FIELDS):
                    current['inspect'] = dict(detail)
        if len(self.events) < 64:
            self.events.append({'side': side, 'phase': current['phase'], 'event': current['status']})

    def emit(self):
        if self.emitted:
            return
        self.emitted = True
        record = {'diagnostic_version': 1, 'provenance': self.provenance,
                  'work': self.outcomes['work'], 'cleanup': self.outcomes['cleanup'], 'events': self.events}
        raw = json.dumps(record, sort_keys=True, separators=(',', ':'))
        if len(raw) <= 16384:
            print(raw)


def new_diagnostics(provenance):
    try:
        return Diagnostics(provenance)
    except Exception as exc:
        if isinstance(exc,Failure) and exc.code in ('PROCESS_TIMEOUT','PROCESS_UNCERTAIN'):
            raise
        return None


def observe(diagnostic, method, *args):
    # Ordinary reporting faults are best-effort. Never consume supervisor
    # cancellation or BaseException control flow (KeyboardInterrupt/SystemExit).
    try:
        if diagnostic is not None:
            getattr(diagnostic, method)(*args)
    except Exception as exc:
        if isinstance(exc,Failure) and exc.code in ('PROCESS_TIMEOUT','PROCESS_UNCERTAIN'):
            raise


def owned_container(v, nonce, expected_id=None):
    require(isinstance(v, dict) and set(v) == set(INSPECT_FIELDS), 'IDENTITY_MISMATCH')
    require(v['Name'] == '/wt-authority-' + nonce and type(v['Labels']) is dict
            and v['Labels'] == {LABEL: nonce}, 'IDENTITY_MISMATCH')
    require(isinstance(v['Id'], str) and HEX64.fullmatch(v['Id']), 'IDENTITY_MISMATCH')
    require(expected_id is None or v['Id'] == expected_id, 'IDENTITY_MISMATCH')
    return v['Id']


def verify_inspect(v, nonce, image_id):
    container_id = owned_container(v, nonce)
    exact = {
        'ConfigImage': IMAGE, 'Image': image_id, 'NetworkMode': 'none', 'PidMode': '',
        'IpcMode': 'private', 'UsernsMode': '', 'UTSMode': '', 'CgroupnsMode': 'private',
        'Privileged': False, 'ReadonlyRootfs': False, 'PublishAllPorts': False,
        'Memory': 1073741824, 'MemorySwap': 1073741824,
        'NanoCpus': 2000000000, 'ShmSize': 67108864, 'Running': True,
        'Runtime': 'runc', 'AutoRemove': False, 'CgroupParent': '', 'VolumeDriver': '',
        'OomKillDisable': False,
        'RestartPolicy': {'Name': 'no', 'MaximumRetryCount': 0},
        'LogConfig': {'Type': 'none', 'Config': {}},
        'SecurityOpt': ['no-new-privileges'], 'CapDrop': ['NET_RAW'], 'Tmpfs': TMPFS,
    }
    require(all(type(v[k]) is type(x) and v[k] == x for k,x in exact.items()), 'RESOURCE_MISMATCH')
    for key in ('Binds', 'VolumesFrom', 'Devices', 'DeviceRequests', 'DeviceCgroupRules', 'CapAdd',
                'GroupAdd', 'Links', 'ExtraHosts', 'Dns', 'DnsOptions', 'DnsSearch'):
        require(v[key] is None or type(v[key]) is list and len(v[key]) == 0, 'RESOURCE_MISMATCH')
    for key in ('Sysctls','StorageOpt'):
        require(v[key] is None or type(v[key]) is dict and not v[key], 'RESOURCE_MISMATCH')
    require(v['PortBindings'] is None or type(v['PortBindings']) is dict and not v['PortBindings'], 'RESOURCE_MISMATCH')
    require(type(v['Ports']) is dict and v['Ports'] in ({}, {'5432/tcp': None}), 'RESOURCE_MISMATCH')
    # Some engines omit HostConfig.Tmpfs from Mounts. A reported mount must be
    # exactly the same tmpfs, never an anonymous image volume or propagation.
    require(type(v['Mounts']) is list and len(v['Mounts']) <= 1, 'RESOURCE_MISMATCH')
    for m in v['Mounts']:
        require(type(m) is dict and set(m) == {'Type','Source','Destination','Mode','RW','Propagation'}, 'RESOURCE_MISMATCH')
        require(m == {'Type':'tmpfs','Source':'','Destination':'/var/lib/postgresql',
                      'Mode':'','RW':True,'Propagation':''}, 'RESOURCE_MISMATCH')
    return container_id


def evidence(raw, tests, test_exit, converter_exit):
    require(type(test_exit) is int and test_exit == 0, 'TEST_PROCESS_FAILED')
    require(type(converter_exit) is int and converter_exit == 0, 'CONVERTER_FAILED')
    require(isinstance(raw, bytes) and 0 < len(raw) <= MAX_JSON, 'EVIDENCE_INVALID')
    known = set(tests)
    ran, passed, proof, locks = set(), set(), set(), set()
    package_started = package_pass = False
    states = set()
    for line in raw.splitlines():
        require(0 < len(line) <= MAX_LINE, 'EVIDENCE_INVALID')
        x = strict_json(line, MAX_LINE)
        require(isinstance(x, dict) and set(x) <= {'Time', 'Action', 'Package', 'Test', 'Elapsed', 'Output', 'FailedBuild'}, 'EVIDENCE_INVALID')
        require(x.get('Package') == PACKAGE and isinstance(x.get('Action'), str), 'EVIDENCE_INVALID')
        if 'Elapsed' in x:
            require(type(x['Elapsed']) in (int, float) and math.isfinite(x['Elapsed']) and x['Elapsed'] >= 0, 'EVIDENCE_INVALID')
        require('FailedBuild' not in x, 'EVIDENCE_INVALID')
        require('Test' not in x or isinstance(x['Test'], str), 'EVIDENCE_INVALID')
        require('Time' not in x or isinstance(x['Time'], str), 'EVIDENCE_INVALID')
        require('Output' not in x or isinstance(x['Output'], str), 'EVIDENCE_INVALID')
        name, action = x.get('Test'), x['Action']
        require(name is None or isinstance(name, str) and name in known, 'EVIDENCE_INVALID')
        require(not package_pass, 'EVIDENCE_INVALID')
        require(action == 'output' or 'Output' not in x, 'EVIDENCE_INVALID')
        if action in ('skip', 'fail', 'build-fail'):
            raise Failure('TEST_FAILED_OR_SKIPPED')
        if action == 'start':
            require(name is None and not package_started, 'EVIDENCE_INVALID')
            package_started = True
        elif action == 'run':
            require(package_started and name in known and name not in ran, 'EVIDENCE_INVALID')
            require('/' not in name or name.rsplit('/',1)[0] in ran and name.rsplit('/',1)[0] not in passed, 'EVIDENCE_INVALID')
            ran.add(name)
        elif action == 'pass':
            require(package_started, 'EVIDENCE_INVALID')
            if name is None:
                require(passed == known, 'EVIDENCE_MISSING')
                package_pass = True
            else:
                require(name in ran and name not in passed, 'EVIDENCE_INVALID')
                require(all(t in passed for t in known if t.startswith(name+'/')), 'EVIDENCE_INVALID')
                passed.add(name)
        elif action == 'output':
            require(package_started and isinstance(x.get('Output'), str), 'EVIDENCE_INVALID')
            require(name is None or name in ran and name not in passed, 'EVIDENCE_INVALID')
            output = x['Output']
            if 'AUTHORITY_' in output:
                require(name in ran and name not in passed, 'EVIDENCE_INVALID')
                m = re.fullmatch(r'\s*(?:authority_pg_(?:integration|fixture)_test\.go:[0-9]+: )?(AUTHORITY_EVIDENCE [A-Za-z0-9_/]+|AUTHORITY_SQLSTATE [0-9A-Z]{5}|AUTHORITY_LOCK_OBSERVED)\n?', output)
                require(m is not None, 'EVIDENCE_INVALID')
                record = m[1]
                if record.startswith('AUTHORITY_EVIDENCE '):
                    require(record == 'AUTHORITY_EVIDENCE ' + name and name not in proof, 'EVIDENCE_INVALID')
                    proof.add(name)
                elif record.startswith('AUTHORITY_SQLSTATE '):
                    code = record.split(' ')[1]
                    require(code in SAFE_STATES and (name, code) not in states, 'EVIDENCE_INVALID')
                    states.add((name, code))
                else:
                    require(name not in locks and name.startswith(tuple(f'TestPGAuthorityA{i}/' for i in range(12,16))), 'EVIDENCE_INVALID')
                    locks.add(name)
        else:
            raise Failure('EVIDENCE_INVALID')
    leaves = {t for t in known if '/' in t and not any(x.startswith(t + '/') for x in known)}
    require(package_pass and ran == passed == known and leaves == proof, 'EVIDENCE_MISSING')
    require(locks == {t for t in leaves if t.startswith(tuple(f'TestPGAuthorityA{i}/' for i in range(12,16)))}, 'EVIDENCE_MISSING')
    return {'parents': len(PARENTS), 'identities': len(known), 'leaves': len(leaves),
            'sqlstates': sorted({code for _, code in states})}


class StreamBudget:
    """Shared real/fake transport accounting; output is never written to disk."""
    def __init__(self, deadline, limit):
        self.deadline, self.limit, self.total = deadline, limit, 0
        self.buffers = {'stdout': bytearray(), 'stderr': bytearray()}

    def tick(self, now):
        require(now < self.deadline, 'PROCESS_TIMEOUT')

    def accept(self, stream, chunk, now):
        self.tick(now)
        require(stream in self.buffers and isinstance(chunk, bytes), 'PROCESS_FAILED')
        self.total += len(chunk)
        require(self.total <= self.limit, 'PROCESS_OUTPUT_LIMIT')
        self.buffers[stream].extend(chunk)


class ProcessIO:
    """OS adapter; capture_process is independently testable with no processes."""
    now = staticmethod(time.monotonic)
    nonblocking = staticmethod(lambda pipe: os.set_blocking(pipe.fileno(),False))
    selector = staticmethod(selectors.DefaultSelector)
    read = staticmethod(os.read)
    write = staticmethod(os.write)

    @staticmethod
    def group_alive(proc):
        try:
            os.killpg(proc.pid,0)
            return True
        except ProcessLookupError:
            return False

    @staticmethod
    def kill_group(proc):
        try:
            os.killpg(proc.pid,signal.SIGKILL)
        except ProcessLookupError:
            pass


def capture_process(proc,data,timeout,limit,io):
    """Drive real/fake pipe transport identically; own process group always stops."""
    budget=StreamBudget(io.now()+timeout,limit)
    try:
        with io.selector() as poll:
            for stream,pipe in [('stdout',proc.stdout),('stderr',proc.stderr)]:
                io.nonblocking(pipe)
                poll.register(pipe,selectors.EVENT_READ,stream)
            offset=0
            if data:
                io.nonblocking(proc.stdin)
                poll.register(proc.stdin,selectors.EVENT_WRITE,'stdin')
            else:
                proc.stdin.close()
            while poll.get_map() or proc.poll() is None:
                budget.tick(io.now())
                for key,_ in poll.select(min(0.05,max(0,budget.deadline-io.now()))):
                    if key.data=='stdin':
                        try:
                            offset+=io.write(key.fd,data[offset:offset+65536])
                        except BrokenPipeError:
                            offset=len(data)
                        if offset==len(data):
                            poll.unregister(key.fileobj)
                            key.fileobj.close()
                    else:
                        chunk=io.read(key.fd,min(65536,limit-budget.total+1))
                        if chunk:
                            budget.accept(key.data,chunk,io.now())
                        else:
                            poll.unregister(key.fileobj)
                            key.fileobj.close()
            budget.tick(io.now())
            proc.wait(timeout=max(0.001,budget.deadline-io.now()))
            require(not io.group_alive(proc),'PROCESS_UNCERTAIN')
            return proc.returncode,bytes(budget.buffers['stdout']),bytes(budget.buffers['stderr'])
    except Failure:
        raise
    except (OSError,ValueError,subprocess.SubprocessError):
        raise Failure('PROCESS_FAILED') from None
    finally:
        # A successful client exit never leaves a writer inheriting our pipes.
        # Docker client termination still needs separate owned-container cleanup.
        try:
            io.kill_group(proc)
            proc.wait(timeout=5)
        except (OSError,subprocess.SubprocessError):
            raise Failure('PROCESS_UNCERTAIN') from None
        finally:
            for pipe in (proc.stdin,proc.stdout,proc.stderr):
                if pipe is not None:
                    pipe.close()


def invoke(argv,*,data=b'',timeout=60,limit=1048576,env=None):
    """Bound combined output with pipes, never unbounded temporary disk files."""
    require(isinstance(argv,list) and argv and all(isinstance(x,str) for x in argv),'ARGUMENTS')
    require(isinstance(data,bytes) and len(data)<=MAX_RAW,'ARGUMENTS')
    require(type(limit) is int and 0<limit<=MAX_JSON and 0<timeout<=900,'ARGUMENTS')
    try:
        proc=subprocess.Popen(argv,stdin=subprocess.PIPE,stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,env=env,close_fds=True,start_new_session=True)
    except (OSError,ValueError,subprocess.SubprocessError):
        raise Failure('PROCESS_FAILED') from None
    return capture_process(proc,data,timeout,limit,ProcessIO())


def checked(argv, **kwargs):
    code, out, _ = invoke(argv, **kwargs)
    require(code == 0, 'PROCESS_FAILED')
    return out


def bounded_file_at(parent_fd, name, limit, mode=0o600):
    fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent_fd)
    try:
        info = os.fstat(fd)
        require(stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid()
                and stat.S_IMODE(info.st_mode) == mode and info.st_nlink == 1
                and info.st_size <= limit, 'PRIVATE_PATH')
        data = os.read(fd, limit + 1)
        require(len(data) <= limit, 'PRIVATE_PATH')
        return data, (info.st_dev, info.st_ino)
    finally:
        os.close(fd)


def private_file_at(parent_fd, name, data):
    require(type(data) is bytes and '/' not in name, 'PRIVATE_PATH')
    fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=parent_fd)
    try:
        with os.fdopen(fd, 'wb', closefd=False) as f:
            f.write(data)
            f.flush()
            os.fsync(fd)
    finally:
        os.close(fd)


class RunState:
    """Pinned private directories and durable single-fixture lease.

    The fixed root prevents bypassing admission by choosing another temp root.
    Lease survives crashes/unknown cleanup; no age-based expiry or force unlock.
    Same-UID/root compromise is outside this fixture's security boundary.
    """
    def __init__(self, root, nonce, create):
        require(isinstance(nonce, str) and HEX32.fullmatch(nonce), 'IDENTITY_MISMATCH')
        self.root = '/tmp/worktool-authority-' + str(os.getuid())
        require(str(root) == self.root, 'PRIVATE_PATH')
        self.nonce, self.name = nonce, 'wt-authority-' + nonce
        self.root_fd = self.fd = None
        self.leased = False
        tmp = os.open('/tmp', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            info = os.fstat(tmp)
            require(info.st_uid == 0 and stat.S_IMODE(info.st_mode) == 0o1777, 'PRIVATE_PATH')
            base = 'worktool-authority-' + str(os.getuid())
            if create:
                try:
                    os.mkdir(base, 0o700, dir_fd=tmp)
                except FileExistsError:
                    pass
            self.root_fd = os.open(base, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=tmp)
            info = os.fstat(self.root_fd)
            require(info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o700, 'PRIVATE_PATH')
            self.value = {'version': 1, 'nonce': nonce, 'name': self.name, 'root': self.root,
                          'socket': '/var/run/docker.sock'}
            if create:
                # An existing lease, even for another nonce, is never removed.
                try:
                    private_file_at(self.root_fd, 'fixture.lock', nonce.encode())
                    self.leased = True
                except FileExistsError:
                    raise Failure('RUN_ALREADY_EXISTS') from None
                # Partial setup is deliberately retained for review, not guessed away.
                os.mkdir(self.name, 0o700, dir_fd=self.root_fd)
            self.fd = os.open(self.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=self.root_fd)
            info = os.fstat(self.fd)
            require(info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o700, 'PRIVATE_PATH')
            if create:
                private_file_at(self.fd, 'state.json', json.dumps(self.value, sort_keys=True).encode())
                private_file_at(self.fd, 'config.json', b'{"auths":{}}')
            self.verify()
        except BaseException:
            self.close()
            if self.leased:
                raise Failure('CLEANUP_UNCONFIRMED') from None
            raise
        finally:
            os.close(tmp)

    def path(self, name=''):
        # Docker/go clients read via the supervisor's pinned directory handle.
        return '/proc/' + str(os.getpid()) + '/fd/' + str(self.fd) + ('/' + name if name else '')

    def verify(self):
        raw, _ = bounded_file_at(self.root_fd, 'fixture.lock', 32)
        require(raw == self.nonce.encode(), 'CLEANUP_IDENTITY_MISMATCH')
        raw, _ = bounded_file_at(self.fd, 'state.json', 4096)
        require(strict_json(raw,4096) == self.value, 'STATE_INVALID')
        current = os.stat(self.name, dir_fd=self.root_fd, follow_symlinks=False)
        pinned = os.fstat(self.fd)
        require((current.st_dev,current.st_ino) == (pinned.st_dev,pinned.st_ino), 'PRIVATE_PATH')

    def has(self,name):
        return name in os.listdir(self.fd)

    def read(self,name,limit):
        return bounded_file_at(self.fd,name,limit)[0]

    def record(self, name, data):
        self.verify()
        private_file_at(self.fd, name, data)

    def remove_confirmed(self):
        self.verify()
        allowed = {'state.json':4096, 'config.json':4096, 'bootstrap.env':8192,
                   'authority.test':128*1024*1024, 'container.id':64, 'daemon.json':4096}
        names = os.listdir(self.fd)
        require(set(names) <= set(allowed), 'CLEANUP_IDENTITY_MISMATCH')
        identities = {}
        for name in names:
            _, identities[name] = bounded_file_at(self.fd,name,allowed[name], 0o700 if name=='authority.test' else 0o600)
        for name in names:
            now = os.stat(name,dir_fd=self.fd,follow_symlinks=False)
            require((now.st_dev,now.st_ino) == identities[name], 'CLEANUP_IDENTITY_MISMATCH')
            os.unlink(name,dir_fd=self.fd)
        # Recheck the pinned directory, then remove only that run directory.
        current = os.stat(self.name,dir_fd=self.root_fd,follow_symlinks=False)
        info = os.fstat(self.fd)
        require((current.st_dev,current.st_ino)==(info.st_dev,info.st_ino), 'PRIVATE_PATH')
        os.rmdir(self.name,dir_fd=self.root_fd)
        raw, identity = bounded_file_at(self.root_fd,'fixture.lock',32)
        require(raw==self.nonce.encode(), 'CLEANUP_IDENTITY_MISMATCH')
        now=os.stat('fixture.lock',dir_fd=self.root_fd,follow_symlinks=False)
        require((now.st_dev,now.st_ino)==identity,'CLEANUP_IDENTITY_MISMATCH')
        os.unlink('fixture.lock',dir_fd=self.root_fd)

    def close(self):
        for attr in ('fd','root_fd'):
            fd=getattr(self,attr,None)
            if fd is not None:
                os.close(fd)
                setattr(self,attr,None)


def trusted_system_node(info, directory):
    """A root-owned chain is immutable to other UIDs; root compromise is excluded."""
    require(info.st_uid == 0 and not info.st_mode & 0o022, 'DOCKER_CONFIGURATION')
    if directory:
        require(stat.S_ISDIR(info.st_mode), 'DOCKER_CONFIGURATION')
    else:
        require(stat.S_ISREG(info.st_mode) and info.st_mode & 0o111
                and not info.st_mode & 0o6000 and info.st_nlink == 1
                and 0 < info.st_size <= 128*1024*1024, 'DOCKER_CONFIGURATION')


def trusted_executable(path):
    """Pin and hash a normalized executable through its trusted directory chain.

    Each path component is opened relative to its already verified descriptor.
    Persisted device/inode identities are rechecked before every invocation and
    recovery cleanup. No mutable user-owned installation is supported.
    """
    path = Path(path)
    require(path.is_absolute() and str(path) == str(path.resolve(strict=True)), 'DOCKER_CONFIGURATION')
    directory = os.open('/', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    binary = None
    chain = []
    try:
        info = os.fstat(directory)
        trusted_system_node(info, True)
        chain.append([info.st_dev, info.st_ino])
        for component in path.parts[1:-1]:
            child = os.open(component, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=directory)
            os.close(directory)
            directory = child
            info = os.fstat(directory)
            trusted_system_node(info, True)
            chain.append([info.st_dev, info.st_ino])
        binary = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
        info = os.fstat(binary)
        trusted_system_node(info, False)
        digest, total = hashlib.sha256(), 0
        while True:
            chunk = os.read(binary, 65536)
            if not chunk:
                break
            total += len(chunk)
            require(total <= 128*1024*1024, 'DOCKER_CONFIGURATION')
            digest.update(chunk)
        after = os.fstat(binary)
        require(total == info.st_size and
                (after.st_size, after.st_mtime_ns, after.st_ctime_ns) ==
                (info.st_size, info.st_mtime_ns, info.st_ctime_ns), 'DOCKER_CONFIGURATION')
        return {'executable': str(path), 'binary_sha256': digest.hexdigest(),
                'executable_device': info.st_dev, 'executable_inode': info.st_ino,
                'executable_ancestors': chain}
    finally:
        if binary is not None:
            os.close(binary)
        os.close(directory)


def docker_preflight():
    require(not any(k.startswith('DOCKER_') for k in os.environ), 'DOCKER_CONFIGURATION')
    # Check persisted context before supplying the explicit default socket. Never
    # silently override a user's nondefault context or credential configuration.
    home = os.environ.get('HOME')
    require(home and Path(home).is_absolute(), 'DOCKER_CONFIGURATION')
    config = Path(home) / '.docker/config.json'
    if config.exists() or config.is_symlink():
        fd = os.open(config,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
        try:
            st=os.fstat(fd)
            require(stat.S_ISREG(st.st_mode) and st.st_size<=65536,'DOCKER_CONFIGURATION')
            value=strict_json(os.read(fd,65537),65536)
            require(type(value) is dict and value.get('currentContext','default') in ('','default'), 'DOCKER_CONFIGURATION')
        finally:
            os.close(fd)
    executable=shutil.which('docker',path='/usr/bin:/bin:/usr/local/bin')
    require(executable is not None,'RUNTIME_UNAVAILABLE')
    executable=str(Path(executable).resolve())
    executable_identity=trusted_executable(executable)
    socket=os.stat('/var/run/docker.sock',follow_symlinks=False)
    require(stat.S_ISSOCK(socket.st_mode), 'DOCKER_CONFIGURATION')
    return dict(executable_identity, socket_device=socket.st_dev, socket_inode=socket.st_ino)


class Docker:
    def __init__(self,state,identity):
        self.state,self.identity=state,identity
        self.env={'PATH':'/usr/bin:/bin','HOME':state.path(),'LANG':'C','LC_ALL':'C'}

    def call(self,args,**kwargs):
        self.state.verify()
        sock=os.stat('/var/run/docker.sock',follow_symlinks=False)
        require(stat.S_ISSOCK(sock.st_mode) and sock.st_dev==self.identity['socket_device']
                and sock.st_ino==self.identity['socket_inode'], 'DOCKER_CONFIGURATION')
        executable_identity=trusted_executable(self.identity['executable'])
        require(all(self.identity.get(k)==v for k,v in executable_identity.items()), 'DOCKER_CONFIGURATION')
        return invoke([self.identity['executable'],'--config',self.state.path(),
                       '--host','unix:///var/run/docker.sock',*args],env=self.env,**kwargs)

    def checked(self,args,**kwargs):
        code,out,err=self.call(args,**kwargs)
        if code != 0:
            failure=Failure('PROCESS_FAILED')
            if args[:3] == ['inspect','--format',INSPECT_TEMPLATE]:
                try:
                    failure.inspect_diagnostic=inspect_failure(code,err)
                except Exception as exc:
                    if isinstance(exc,Failure) and exc.code in ('PROCESS_TIMEOUT','PROCESS_UNCERTAIN'):
                        raise
            raise failure
        return out


def cleanup_owned(state,docker,budget,diagnostic=None):
    observe(diagnostic,'phase','cleanup','CLEANUP_STATE')
    state.verify()
    # json-per-row output is bounded by the transport before parsing.
    observe(diagnostic,'phase','cleanup','CLEANUP_LIST')
    raw=docker.checked(['container','ls','-a','--filter','name=^/'+state.name+'$',
                        '--format','{{json .}}'],timeout=budget(10))
    rows=[strict_json(line,8192) for line in raw.splitlines() if line]
    require(len(rows)<=1,'CLEANUP_IDENTITY_MISMATCH')
    if rows:
        require(type(rows[0]) is dict and rows[0].get('Names')==state.name,'CLEANUP_IDENTITY_MISMATCH')
        observe(diagnostic,'phase','cleanup','CLEANUP_INSPECT')
        info=strict_json(docker.checked(['inspect','--format',INSPECT_TEMPLATE,state.name],timeout=budget(10)),16384)
        expected=None
        if state.has('container.id'):
            raw=state.read('container.id',64)
            expected=raw.decode('ascii')
        cid=owned_container(info,state.nonce,expected)
        observe(diagnostic,'phase','cleanup','CLEANUP_REMOVE')
        docker.checked(['rm','-f',cid],timeout=budget(20))
    observe(diagnostic,'phase','cleanup','CLEANUP_ABSENCE')
    raw=docker.checked(['container','ls','-a','--filter','name=^/'+state.name+'$',
                        '--format','{{json .}}'],timeout=budget(10))
    require(not raw.strip(),'CLEANUP_UNCONFIRMED')
    observe(diagnostic,'phase','cleanup','CLEANUP_STATE_REMOVE')
    state.remove_confirmed()


def finalize_owned(work,cleanup_action,diagnostic=None):
    """Original failure precedence, with cancellation-safe diagnostic boundaries."""
    result, failure, cleanup_failure = None, None, None
    try:
        observe(diagnostic,'phase','work','WORK')
        result=work()
    except BaseException as exc:
        failure=exc if isinstance(exc,Failure) else Failure('UNEXPECTED_FAILURE')
    # Reporting cancellation must stop acceptance, but must not skip the one
    # cleanup attempt or replace an already established work failure.
    try:
        observe(diagnostic,'finish','work',failure)
        observe(diagnostic,'phase','cleanup','CLEANUP')
    except BaseException as exc:
        if failure is None:
            failure=exc if isinstance(exc,Failure) else Failure('UNEXPECTED_FAILURE')
    try:
        cleanup_action()
    except BaseException as exc:
        cleanup_failure=exc
    try:
        observe(diagnostic,'finish','cleanup',cleanup_failure)
        observe(diagnostic,'emit')
    except BaseException as exc:
        if failure is None:
            failure=exc if isinstance(exc,Failure) else Failure('UNEXPECTED_FAILURE')
    if cleanup_failure is not None:
        raise Failure('CLEANUP_UNCONFIRMED') from None
    if failure:
        raise failure
    return result


def go_env():
    env = {k: v for k, v in os.environ.items() if k in ('PATH', 'HOME', 'GOMODCACHE', 'GOCACHE', 'GOPATH', 'TMPDIR', 'LANG')}
    env.update(GOENV='off', GOTOOLCHAIN='local', GOWORK='off', GOPROXY='off', GOSUMDB='off',
               GOMAXPROCS='2', GOTELEMETRY='off', GOFLAGS='-mod=readonly',
               CGO_ENABLED='0', GOOS='linux', GOARCH='amd64')
    return env


TOOLCHAIN_SHA256 = 'da3e906bf3b01e50e398146823c6bf2b16a22951597834c5f4808ec38ca5e308'
GO_BINARY_SHA256 = '5f5eb6d0b2fb6aa0bb5d5f8c5f1c944e0de955c925f0111af92a9d8f79680b5b'


def toolchain_identity(go):
    go=Path(go)
    require(go.is_absolute() and go.is_file() and go.parent.name=='bin' and go.name=='go', 'ARGUMENTS')
    require(sha(go.read_bytes())==GO_BINARY_SHA256,'TOOLCHAIN_HASH')
    digest=hashlib.sha256()
    for path in sorted(go.parent.parent.rglob('*')):
        require(not path.is_symlink(),'TOOLCHAIN_HASH')
        if path.is_file():
            digest.update(path.relative_to(go.parent.parent).as_posix().encode()+b'\0'+hashlib.sha256(path.read_bytes()).digest())
    require(digest.hexdigest()==TOOLCHAIN_SHA256,'TOOLCHAIN_HASH')
    return str(go)


def build_test_binary(state,go,budget):
    env=go_env()
    require(checked([go,'env','GOVERSION'],env=env,timeout=budget(10)).strip()==GO_VERSION.encode(),'GO_VERSION')
    backend=ROOT/'backend-go'
    checked([go,'-C',str(backend),'mod','verify'],env=env,timeout=budget(60))
    binary=state.path('authority.test')
    checked([go,'-C',str(backend),'test','-c','-tags=authority_pg','-trimpath',
             '-buildvcs=false','-o',binary,'./internal/callback/durableauthority'],env=env,timeout=budget(180),limit=MAX_RAW)
    # Build output is an exclusively owned file, and must not be a link.
    fd=os.open('authority.test',os.O_RDONLY|os.O_NOFOLLOW,dir_fd=state.fd)
    try:
        info=os.fstat(fd)
        require(stat.S_ISREG(info.st_mode) and info.st_nlink==1 and info.st_uid==os.getuid()
                and info.st_size<=128*1024*1024,'PRIVATE_PATH')
        os.fchmod(fd,0o700)
    finally:
        os.close(fd)
    raw,_=bounded_file_at(state.fd,'authority.test',128*1024*1024,0o700)
    return binary,sha(raw)


def fixture_work(state,docker,go,tests,budget,diagnostic=None):
    env=go_env()
    observe(diagnostic,'phase','work','BUILD')
    binary,binary_hash=build_test_binary(state,go,budget)
    observe(diagnostic,'phase','work','IMAGE_CHECK')
    image_raw=docker.checked(['image','inspect','--format',
        '{"Id":{{json .Id}},"RepoDigests":{{json .RepoDigests}}}',IMAGE],timeout=budget(10))
    image=strict_json(image_raw,16384)
    require(type(image) is dict and set(image)=={'Id','RepoDigests'} and type(image['Id']) is str
            and image['Id'].startswith('sha256:') and HEX64.fullmatch(image['Id'][7:]),'IMAGE_MISMATCH')
    require(type(image['RepoDigests']) is list and all(type(x) is str for x in image['RepoDigests'])
            and any(x in image['RepoDigests'] for x in (IMAGE,'docker.io/library/'+IMAGE)),'IMAGE_MISMATCH')
    observe(diagnostic,'phase','work','CREATE')
    nonce=state.nonce
    target=validate_target({'version':1,'run_nonce':nonce,'database':'wt_authority_test_'+nonce,
        'user':'wt_authority_owner_'+nonce,'password':secrets.token_hex(32),'host':'127.0.0.1','port':5432,
        'schema_sha256':SCHEMA_SHA256,'binary_sha256':binary_hash,'image':IMAGE,
        'network_mode':'none','pid_mode':'','cpu_nanos':2000000000,'memory_bytes':1073741824,
        'memory_swap_bytes':1073741824,'shm_bytes':67108864,'tmpfs_bytes':2147483648,
        'no_mounts_or_ports':True,'run_owned':True})
    state.record('bootstrap.env',('POSTGRES_USER='+target['user']+'\nPOSTGRES_DB='+target['database']+
        '\nPOSTGRES_PASSWORD='+target['password']+'\nPGDATA=/var/lib/postgresql/18/docker\n').encode())
    created=docker.checked(['run','--pull=never','--detach','--name',state.name,'--label',LABEL+'='+nonce,
        '--platform','linux/amd64','--network','none','--ipc','private','--cgroupns','private',
        '--runtime','runc','--security-opt','no-new-privileges','--cap-drop','NET_RAW',
        '--log-driver','none','--restart','no','--cpus','2','--memory','1g','--memory-swap','1g',
        '--shm-size','64m','--tmpfs','/var/lib/postgresql:rw,nosuid,nodev,size=2147483648',
        '--env-file',state.path('bootstrap.env'),IMAGE,'postgres','-c','listen_addresses=127.0.0.1',
        '-c','max_connections=10','-c','statement_timeout=5000','-c','lock_timeout=1000',
        '-c','idle_in_transaction_session_timeout=5000'],timeout=budget(30))
    require(HEX64.fullmatch(created.decode('ascii').strip()) is not None,'IDENTITY_MISMATCH')
    container_id=created.strip().decode('ascii')
    state.record('container.id',container_id.encode())
    def inspect():
        info=strict_json(docker.checked(['inspect','--format',INSPECT_TEMPLATE,state.name],timeout=budget(10)),16384)
        require(verify_inspect(info,nonce,image['Id'])==container_id,'IDENTITY_MISMATCH')
    observe(diagnostic,'phase','work','RESOURCE_INSPECT')
    inspect()
    observe(diagnostic,'phase','work','READINESS')
    ready_deadline=time.monotonic()+min(60,budget(60))
    while True:
        remaining=ready_deadline-time.monotonic()
        require(remaining>0,'READINESS_TIMEOUT')
        code,_,_=docker.call(['exec',container_id,'pg_isready','-h','127.0.0.1','-p','5432',
                             '-U',target['user'],'-d',target['database']],timeout=min(5,remaining,budget(5)))
        if code==0:
            break
        time.sleep(min(0.1,max(0,ready_deadline-time.monotonic())))
    observe(diagnostic,'phase','work','RESOURCE_RECHECK')
    inspect()
    # These are fixed allowlisted paths/commands, not user-provided shell input.
    # Authenticated actual data_directory checks happen again in Go before DDL.
    observe(diagnostic,'phase','work','PATH_CHECK')
    for path in ('/var/lib/postgresql/18/docker','/var/lib/postgresql/18/docker/pg_wal'):
        resolved=docker.checked(['exec',container_id,'readlink','-e',path],timeout=budget(5))
        require(resolved==path.encode()+b'\n','RESOURCE_MISMATCH')
    observe(diagnostic,'phase','work','COPY_BINARY')
    destination='/var/lib/postgresql/authority-'+nonce+'.test'
    docker.checked(['cp',binary,container_id+':'+destination],timeout=budget(15))
    # Bootstrap Config.Env contains the synthetic password, visible to admins.
    # The test exec environment and argv contain no password or DSN.
    observe(diagnostic,'phase','work','TEST_EXECUTION')
    test_exit,raw,_=docker.call(['exec','-i',container_id,'env','-i',
        'WORKTOOL_AUTHORITY_PG_STAGE_A=1','WORKTOOL_AUTHORITY_PG_NONCE='+nonce,
        'GOMAXPROCS=2','TZ=UTC',destination,'-test.run='+TEST_RE,
        '-test.count=1','-test.timeout=10m0s','-test.v=test2json'],
        data=json.dumps(target,separators=(',',':')).encode(),timeout=budget(600),limit=MAX_RAW)
    observe(diagnostic,'phase','work','EVIDENCE_VALIDATION')
    converter_exit,converted,_=invoke([go,'tool','test2json','-p',PACKAGE],data=raw,
        env=env,timeout=budget(30),limit=MAX_JSON)
    result=evidence(converted,tests,test_exit,converter_exit)
    # Verify the full tree again: converter/compiler drift is not accepted.
    observe(diagnostic,'phase','work','TOOLCHAIN_RECHECK')
    toolchain_identity(go)
    return {'schema_sha256':SCHEMA_SHA256,'binary_sha256':binary_hash,
            'manifest_sha256':MANIFEST_SHA256,'toolchain_sha256':TOOLCHAIN_SHA256,
            'go_binary_sha256':GO_BINARY_SHA256,'image':IMAGE,'result':result,
            'scope':'owner-only-no-authority'}


def run(root,go,nonce,execute,allow_pull):
    require(execute is True,'EXECUTION_NOT_APPROVED')
    require(allow_pull is False,'ARGUMENTS')
    # This candidate intentionally supports pre-acquired pinned image only.
    # Acquisition requires a separately reviewed route rather than hidden fallback.
    # Reserve the final five seconds for process-group kill/reap after the
    # absolute supervisor alarm. Cleanup gets its own sixty-second reserve.
    deadline=time.monotonic()+895
    old_alarm=signal.getsignal(signal.SIGALRM)
    def expired(signum,frame):
        raise Failure('PROCESS_TIMEOUT')
    signal.signal(signal.SIGALRM,expired)
    signal.setitimer(signal.ITIMER_REAL,835)
    try:
        return run_supervised(root,go,nonce,deadline)
    finally:
        signal.setitimer(signal.ITIMER_REAL,0)
        signal.signal(signal.SIGALRM,old_alarm)


def run_supervised(root,go,nonce,deadline):
    def budget(maximum,reserve=60):
        left=deadline-time.monotonic()-reserve
        require(left>0,'PROCESS_TIMEOUT')
        return min(left,maximum)
    tests=manifest((ROOT/'scripts/authority-pg-ci.cases.json').read_bytes(),MANIFEST_SHA256)
    require(sha((ROOT/'backend-go/internal/callback/durableauthority/testdata/schema_candidate.sql').read_bytes())==SCHEMA_SHA256,'SOURCE_DRIFT')
    go=toolchain_identity(go)
    docker_identity=docker_preflight()
    # Construct before acquiring owned state: cancellation here has nothing
    # from this run to clean and cannot leave an acquired lease unclosed.
    diagnostic=new_diagnostics('RUN')
    state=RunState(root,nonce,True)
    old_handlers={}
    def interrupted(signum,frame):
        raise Failure('PROCESS_UNCERTAIN')
    try:
        for sig in (signal.SIGTERM,signal.SIGINT,signal.SIGHUP):
            old_handlers[sig]=signal.signal(sig,interrupted)
        docker=Docker(state,docker_identity)
        def work():
            state.record('daemon.json',json.dumps(docker_identity,sort_keys=True).encode())
            return fixture_work(state,docker,go,tests,budget,diagnostic)
        def clean():
            signal.setitimer(signal.ITIMER_REAL,max(0.001,deadline-time.monotonic()))
            # Ignore further catchable interruption while doing bounded cleanup.
            for sig in old_handlers:
                signal.signal(sig,signal.SIG_IGN)
            cleanup_owned(state,docker,lambda n:budget(n,0),diagnostic)
        report=finalize_owned(work,clean,diagnostic)
        report['status']='TESTS_AND_CLEANUP_CONFIRMED'
        print(json.dumps(report,sort_keys=True,separators=(',',':')))
    finally:
        for sig,handler in old_handlers.items():
            signal.signal(sig,handler)
        state.close()


def cleanup_recovery(root,nonce,diagnostic):
    # Explicit recovery only; there is no shell trap or unowned cleanup path.
    observe(diagnostic,'phase','cleanup','CLEANUP_PREFLIGHT')
    current=docker_preflight()
    observe(diagnostic,'phase','cleanup','CLEANUP_STATE')
    state=RunState(root,nonce,False)
    try:
        raw,_=bounded_file_at(state.fd,'daemon.json',4096)
        recorded=strict_json(raw,4096)
        require(recorded==current,'CLEANUP_IDENTITY_MISMATCH')
        deadline=time.monotonic()+60
        def budget(n):
            left=deadline-time.monotonic()
            require(left>0,'CLEANUP_UNCONFIRMED')
            return min(n,left)
        cleanup_owned(state,Docker(state,recorded),budget,diagnostic)
        return 'AUTHORITY_PG_CLEANUP_CONFIRMED'
    finally:
        state.close()


def cleanup(root,nonce):
    diagnostic=new_diagnostics('RECOVERY')
    # Cancellation before recovery begins must not start recovery operations.
    observe(diagnostic,'phase','cleanup','CLEANUP')
    failure=None
    try:
        result=cleanup_recovery(root,nonce,diagnostic)
    except BaseException as exc:
        failure=exc
    try:
        observe(diagnostic,'finish','cleanup',failure)
        observe(diagnostic,'emit')
    except BaseException:
        if failure is None:
            raise
    if failure is not None:
        raise failure
    return result


# Frozen identities are repeated deliberately so pure tests exercise every mandatory
# leaf without opening a file or executing a process. Manifest bytes are pinned.
CASES = tuple("""TestPGAuthorityA01
TestPGAuthorityA01/rollback_absence
TestPGAuthorityA01/commit_catalog
TestPGAuthorityA01/duplicate_install
TestPGAuthorityA02
TestPGAuthorityA02/scope_invalid
TestPGAuthorityA02/scope_overbound
TestPGAuthorityA02/allocation_invalid
TestPGAuthorityA02/allocation_overbound
TestPGAuthorityA02/instance_invalid
TestPGAuthorityA02/instance_overbound
TestPGAuthorityA02/boot_invalid
TestPGAuthorityA02/boot_overbound
TestPGAuthorityA02/epoch_invalid
TestPGAuthorityA02/epoch_overbound
TestPGAuthorityA02/provenance_invalid
TestPGAuthorityA02/provenance_overbound
TestPGAuthorityA02/revision_zero
TestPGAuthorityA02/plan_shape
TestPGAuthorityA02/digest_shape
TestPGAuthorityA02/plan_empty
TestPGAuthorityA02/plan_overbound
TestPGAuthorityA02/descriptors_empty
TestPGAuthorityA02/descriptors_overbound
TestPGAuthorityA02/not_null
TestPGAuthorityA02/valid_limits
TestPGAuthorityA03
TestPGAuthorityA03/ledger_scope
TestPGAuthorityA03/ledger_allocation
TestPGAuthorityA03/ledger_instance
TestPGAuthorityA03/ledger_boot
TestPGAuthorityA03/ledger_epoch
TestPGAuthorityA03/ledger_revision
TestPGAuthorityA03/ledger_digest
TestPGAuthorityA03/event_scope
TestPGAuthorityA03/event_allocation
TestPGAuthorityA03/event_instance
TestPGAuthorityA03/event_boot
TestPGAuthorityA03/event_epoch
TestPGAuthorityA03/event_revision
TestPGAuthorityA03/event_digest
TestPGAuthorityA03/event_absent_ledger
TestPGAuthorityA04
TestPGAuthorityA04/registry_scope
TestPGAuthorityA04/ledger_scope
TestPGAuthorityA04/global_operation
TestPGAuthorityA05
TestPGAuthorityA05/registry_update
TestPGAuthorityA05/registry_self_update
TestPGAuthorityA05/registry_delete
TestPGAuthorityA05/events_update
TestPGAuthorityA05/events_self_update
TestPGAuthorityA05/events_delete
TestPGAuthorityA05/truncate_registry_collective
TestPGAuthorityA05/truncate_ledger_collective
TestPGAuthorityA05/truncate_events
TestPGAuthorityA05/ledger_delete
TestPGAuthorityA06
TestPGAuthorityA06/initial_halted
TestPGAuthorityA06/running_self_update
TestPGAuthorityA06/identity_scope_id
TestPGAuthorityA06/identity_allocation_id
TestPGAuthorityA06/identity_instance_id
TestPGAuthorityA06/identity_boot_id
TestPGAuthorityA06/identity_epoch_id
TestPGAuthorityA06/identity_registry_revision
TestPGAuthorityA06/identity_binding_identity
TestPGAuthorityA06/valid_JOINED
TestPGAuthorityA06/valid_UNCERTAINTY
TestPGAuthorityA06/valid_CLEANUP_UNCONFIRMED
TestPGAuthorityA06/valid_START_INTERRUPTED
TestPGAuthorityA06/halted_to_running
TestPGAuthorityA06/halted_replace_reason
TestPGAuthorityA06/halted_self_update
TestPGAuthorityA07
TestPGAuthorityA07/incident_UNCERTAINTY
TestPGAuthorityA07/incident_CLEANUP_UNCONFIRMED
TestPGAuthorityA07/incident_START_INTERRUPTED
TestPGAuthorityA07/incident_JOINED
TestPGAuthorityA07/incident_EVIDENCE_RECORDED
TestPGAuthorityA07/incident_UNKNOWN
TestPGAuthorityA07/joined_UNCERTAINTY
TestPGAuthorityA07/joined_CLEANUP_UNCONFIRMED
TestPGAuthorityA07/joined_START_INTERRUPTED
TestPGAuthorityA07/joined_JOINED
TestPGAuthorityA07/joined_EVIDENCE_RECORDED
TestPGAuthorityA07/joined_UNKNOWN
TestPGAuthorityA07/reconciliation_fact_UNCERTAINTY
TestPGAuthorityA07/reconciliation_fact_CLEANUP_UNCONFIRMED
TestPGAuthorityA07/reconciliation_fact_START_INTERRUPTED
TestPGAuthorityA07/reconciliation_fact_JOINED
TestPGAuthorityA07/reconciliation_fact_EVIDENCE_RECORDED
TestPGAuthorityA07/reconciliation_fact_UNKNOWN
TestPGAuthorityA07/unknown_UNCERTAINTY
TestPGAuthorityA07/unknown_CLEANUP_UNCONFIRMED
TestPGAuthorityA07/unknown_START_INTERRUPTED
TestPGAuthorityA07/unknown_JOINED
TestPGAuthorityA07/unknown_EVIDENCE_RECORDED
TestPGAuthorityA07/unknown_UNKNOWN
TestPGAuthorityA07/operation_invalid
TestPGAuthorityA07/operation_overbound
TestPGAuthorityA07/reference_invalid
TestPGAuthorityA07/reference_overbound
TestPGAuthorityA07/not_null
TestPGAuthorityA08
TestPGAuthorityA08/missing_registry
TestPGAuthorityA08/valid_absent
TestPGAuthorityA08/second_intent
TestPGAuthorityA08/changed_allocation
TestPGAuthorityA08/changed_instance
TestPGAuthorityA08/changed_boot
TestPGAuthorityA08/changed_epoch
TestPGAuthorityA08/changed_provenance
TestPGAuthorityA08/changed_revision
TestPGAuthorityA08/changed_plan
TestPGAuthorityA08/changed_descriptor
TestPGAuthorityA09
TestPGAuthorityA09/history_JOINED
TestPGAuthorityA09/history_UNCERTAINTY
TestPGAuthorityA09/history_CLEANUP_UNCONFIRMED
TestPGAuthorityA09/history_START_INTERRUPTED
TestPGAuthorityA09/history_EVIDENCE_RECORDED
TestPGAuthorityA10
TestPGAuthorityA10/absent
TestPGAuthorityA10/running
TestPGAuthorityA10/joined
TestPGAuthorityA10/incident
TestPGAuthorityA10/running_terminal
TestPGAuthorityA10/halt_without_event
TestPGAuthorityA10/corrupt_plan
TestPGAuthorityA10/corrupt_descriptors
TestPGAuthorityA10/corrupt_hash
TestPGAuthorityA11
TestPGAuthorityA11/direct_rollback
TestPGAuthorityA11/before_halt
TestPGAuthorityA11/before_readback
TestPGAuthorityA11/aborted_transaction
TestPGAuthorityA12
TestPGAuthorityA12/intent_unique_r1
TestPGAuthorityA12/intent_unique_r2
TestPGAuthorityA12/intent_unique_r3
TestPGAuthorityA13
TestPGAuthorityA13/joined_observe_r1
TestPGAuthorityA13/joined_observe_r2
TestPGAuthorityA13/joined_observe_r3
TestPGAuthorityA13/incident_observe_r1
TestPGAuthorityA13/incident_observe_r2
TestPGAuthorityA13/incident_observe_r3
TestPGAuthorityA13/two_events_r1
TestPGAuthorityA13/two_events_r2
TestPGAuthorityA13/two_events_r3
TestPGAuthorityA14
TestPGAuthorityA14/global_operation_r1
TestPGAuthorityA14/global_operation_r2
TestPGAuthorityA14/global_operation_r3
TestPGAuthorityA15
TestPGAuthorityA15/lock_timeout_r1
TestPGAuthorityA15/lock_timeout_r2
TestPGAuthorityA15/lock_timeout_r3
TestPGAuthorityA15/server_cancel_r1
TestPGAuthorityA15/server_cancel_r2
TestPGAuthorityA15/server_cancel_r3
TestPGAuthorityA16
TestPGAuthorityA16/release_before_receipt
TestPGAuthorityA16/commit_acknowledgement_injection
TestPGAuthorityA16/rollback_cleanup_injection""".splitlines())


def synthetic_inspect(nonce):
    return {'Id':'4'*64,'Name':'/wt-authority-'+nonce,'Image':'sha256:'+'5'*64,
        'ConfigImage':IMAGE,'Labels':{LABEL:nonce},'NetworkMode':'none','PidMode':'',
        'IpcMode':'private','UsernsMode':'','UTSMode':'','CgroupnsMode':'private',
        'Privileged':False,'ReadonlyRootfs':False,'PublishAllPorts':False,
        'Memory':1073741824,'MemorySwap':1073741824,'NanoCpus':2000000000,
        'ShmSize':67108864,'Running':True,'Runtime':'runc','AutoRemove':False,'CgroupParent':'','VolumeDriver':'',
        'OomKillDisable':False,'Sysctls':None,'StorageOpt':None,'GroupAdd':None,
        'Links':None,'ExtraHosts':None,'Dns':[],'DnsOptions':[],'DnsSearch':[],
        'RestartPolicy':{'Name':'no','MaximumRetryCount':0},
        'LogConfig':{'Type':'none','Config':{}},'SecurityOpt':['no-new-privileges'],
        'CapDrop':['NET_RAW'],'Tmpfs':dict(TMPFS),'Binds':None,'VolumesFrom':None,
        'Devices':[],'DeviceRequests':None,'DeviceCgroupRules':None,'CapAdd':None,'PortBindings':{},
        'Ports':{'5432/tcp':None},'Mounts':[]}


def synthetic_events(tests):
    events=[{'Action':'start','Package':PACKAGE}]
    for parent in PARENTS:
        events.append({'Action':'run','Package':PACKAGE,'Test':parent})
        for name in (t for t in tests if t.startswith(parent+'/')):
            events.append({'Action':'run','Package':PACKAGE,'Test':name})
            if name.startswith(tuple(f'TestPGAuthorityA{i}/' for i in range(12,16))):
                events.append({'Action':'output','Package':PACKAGE,'Test':name,'Output':'    authority_pg_fixture_test.go:1: AUTHORITY_LOCK_OBSERVED\n'})
            events.append({'Action':'output','Package':PACKAGE,'Test':name,'Output':'    authority_pg_integration_test.go:1: AUTHORITY_EVIDENCE '+name+'\n'})
            events.append({'Action':'pass','Package':PACKAGE,'Test':name})
        events.append({'Action':'pass','Package':PACKAGE,'Test':parent})
    events.append({'Action':'pass','Package':PACKAGE})
    return events


def parse_arguments(argv):
    if argv==['--selftest']:
        return ('selftest',)
    if len(argv)==3 and argv[0]=='cleanup':
        require(HEX32.fullmatch(argv[2]) is not None,'ARGUMENTS')
        return ('cleanup',argv[1],argv[2])
    # No nonce, trap, filesystem write or Docker call occurs before this gate.
    require(len(argv)==4 and argv[0]=='run' and argv[3]=='--execute-reviewed-stage-a','ARGUMENTS')
    require(Path(argv[1]).is_absolute() and Path(argv[2]).is_absolute(),'ARGUMENTS')
    return ('run',argv[1],argv[2])


def selftest():
    """In-memory records only; external subprocess entrypoints remain forbidden."""
    old={name:globals()[name] for name in ('invoke','build_test_binary','toolchain_identity')}
    process_names=('Popen','run','call','check_call','check_output')
    old_process={name:getattr(subprocess,name) for name in process_names}
    def forbidden(*args,**kwargs):
        raise AssertionError('subprocess forbidden in selftest')
    for name in process_names:
        setattr(subprocess,name,forbidden)
    globals()['invoke']=forbidden
    checks=0
    def rejects(fn,*args,code=None):
        nonlocal checks
        try:
            fn(*args)
        except Failure as exc:
            assert code is None or exc.code==code
            assert str(exc) in SAFE_CODES
            checks+=1
            return
        raise AssertionError('expected rejection')
    encode=lambda xs:b'\n'.join(json.dumps(x).encode() for x in xs)
    try:
        nonce='1'*32
        raw_manifest=(json.dumps({'version':1,'package':PACKAGE,'tests':list(CASES)},indent=2)+'\n').encode()
        assert manifest(raw_manifest,MANIFEST_SHA256)==CASES
        checks+=1
        rejects(manifest,raw_manifest+b' ',MANIFEST_SHA256)
        rejects(strict_json,b'{"version":1,"version":1}',4096)
        events=synthetic_events(CASES)
        assert evidence(encode(events),CASES,0,0)['leaves']==150
        checks+=1
        rejects(evidence,encode(events),CASES,1,0,code='TEST_PROCESS_FAILED')
        rejects(evidence,encode(events),CASES,0,1,code='CONVERTER_FAILED')
        rejects(evidence,encode(events),CASES,False,0)
        # Every missing identity/leaf and every missing proof/lock is rejected.
        for name in CASES:
            rejects(evidence,encode([x for x in events if x.get('Test')!=name]),CASES,0,0)
        for index,x in enumerate(events):
            if x['Action']=='output':
                rejects(evidence,encode(events[:index]+events[index+1:]),CASES,0,0)
        leaf=CASES[1]
        sample=next(i for i,x in enumerate(events) if x.get('Test')==leaf and x['Action']=='output')
        for payload in ('AUTHORITY_UNKNOWN secret-must-not-escape\n',
                        'AUTHORITY_SQLSTATE 23514 secret-must-not-escape\n',
                        'AUTHORITY_SQLSTATE ZZZZZ\n','AUTHORITY_EVIDENCE secret-must-not-escape\n',
                        'AUTHORITY_LOCK_OBSERVED\n'):
            bad=[dict(x) for x in events];bad[sample]['Output']=payload
            rejects(evidence,encode(bad),CASES,0,0)
        for action in ('skip','fail','build-fail','pause','cont','unknown'):
            bad=[dict(x) for x in events];bad[sample]['Action']=action
            rejects(evidence,encode(bad),CASES,0,0)
        for value in (None,False,'secret',{},[]):
            bad=[dict(x) for x in events];bad[0]['FailedBuild']=value
            rejects(evidence,encode(bad),CASES,0,0)
        bad=[dict(x) for x in events];bad[0]['Test']=None
        rejects(evidence,encode(bad),CASES,0,0)
        bad=list(events);bad[sample],bad[sample+1]=bad[sample+1],bad[sample]
        rejects(evidence,encode(bad),CASES,0,0)
        rejects(evidence,encode(events+[events[-1]]),CASES,0,0)
        for key in INSPECT_FIELDS:
            fixture=synthetic_inspect(nonce);del fixture[key]
            rejects(verify_inspect,fixture,nonce,'sha256:'+'5'*64)
            fixture=synthetic_inspect(nonce);fixture[key]='drift'
            rejects(verify_inspect,fixture,nonce,'sha256:'+'5'*64)
        fixture=synthetic_inspect(nonce)
        assert verify_inspect(fixture,nonce,fixture['Image'])=='4'*64
        rejects(verify_inspect,dict(fixture,Unknown=True),nonce,fixture['Image'])
        for field in ('IpcMode','UsernsMode','PidMode','NetworkMode'):
            rejects(verify_inspect,dict(fixture,**{field:'host'}),nonce,fixture['Image'])
        for mount in ({'Type':'bind','Destination':'/var/lib/postgresql'},
                      {'Type':'tmpfs','Source':'','Destination':'/var/lib/postgresql','Mode':'','RW':True,'Propagation':'shared'}):
            rejects(verify_inspect,dict(fixture,Mounts=[mount]),nonce,fixture['Image'])
        # Validation runs before all resource paths. Rejected forms never dispatch.
        for args in ([],['run','/r','/g'],['run','/r','/g','--allow-image-pull'],
                     ['run','/r','/g','--bogus'],['run','/r','/g','--execute-reviewed-stage-a','--bogus']):
            rejects(parse_arguments,args)
        rejects(run,'/unused','/unused',nonce,False,False)
        def node(uid=0,mode=stat.S_IFREG|0o755,size=1024,links=1):
            return type('Node',(),{'st_uid':uid,'st_mode':mode,'st_size':size,'st_nlink':links})()
        trusted_system_node(node(),False);checks+=1
        trusted_system_node(node(mode=stat.S_IFDIR|0o755),True);checks+=1
        for bad in (node(uid=1000),node(mode=stat.S_IFREG|0o775),
                    node(mode=stat.S_IFREG|0o777),node(mode=stat.S_IFREG|0o4755),
                    node(mode=stat.S_IFLNK|0o777),node(mode=stat.S_IFREG|0o644),
                    node(size=0),node(size=128*1024*1024+1),node(links=2)):
            rejects(trusted_system_node,bad,False)
        for bad in (node(uid=1000,mode=stat.S_IFDIR|0o755),
                    node(mode=stat.S_IFDIR|0o775),node(mode=stat.S_IFDIR|0o1777),node()):
            rejects(trusted_system_node,bad,True)
        counter=StreamBudget(10,8)
        counter.accept('stdout',b'1234',1)
        counter.accept('stderr',b'5678',2)
        rejects(counter.accept,'stderr',b'9',3,code='PROCESS_OUTPUT_LIMIT')
        rejects(StreamBudget(10,8).tick,10,code='PROCESS_TIMEOUT')
        class Pipe:
            def __init__(self,number):self.number=number;self.closed=False
            def fileno(self):return self.number
            def close(self):self.closed=True
        class Proc:
            def __init__(self):
                self.stdin=Pipe(0);self.stdout=Pipe(1);self.stderr=Pipe(2)
                self.returncode=0;self.killed=False;self.waits=0
            def poll(self):return 0
            def wait(self,timeout):self.waits+=1;return 0
        class Reactor:
            def __init__(self,proc,chunks,stalled=False,orphan=False,kill_failure=False):
                self.proc=proc;self.chunks={k:list(v) for k,v in chunks.items()}
                self.stalled=stalled;self.orphan=orphan;self.kill_failure=kill_failure
                self.clock=0;self.entries={};self.written=b''
            def now(self):return self.clock
            def nonblocking(self,pipe):pass
            def selector(self):return self
            def __enter__(self):return self
            def __exit__(self,*args):pass
            def register(self,pipe,event,name):
                self.entries[pipe.number]=type('Key',(),{'fd':pipe.number,'fileobj':pipe,'data':name})()
            def unregister(self,pipe):del self.entries[pipe.number]
            def get_map(self):return self.entries
            def select(self,timeout):
                self.clock+=timeout if self.stalled else 0.001
                return [] if self.stalled else [(key,None) for key in list(self.entries.values())]
            def read(self,fd,size):
                chunks=self.chunks.setdefault(fd,[])
                if not chunks:return b''
                data=chunks.pop(0)
                if len(data)>size:chunks.insert(0,data[size:])
                return data[:size]
            def write(self,fd,data):self.written+=data;return len(data)
            def group_alive(self,proc):return self.orphan
            def kill_group(self,proc):
                proc.killed=True
                if self.kill_failure:raise OSError('private-secret')
        for mode,expected in (('success',None),('combined_overflow','PROCESS_OUTPUT_LIMIT'),
                              ('deadline','PROCESS_TIMEOUT'),('orphan','PROCESS_UNCERTAIN'),
                              ('kill_failure','PROCESS_UNCERTAIN')):
            proc=Proc()
            io=Reactor(proc,{1:[b'12345'],2:[b'67890' if mode=='combined_overflow' else b'6']},
                       stalled=mode=='deadline',orphan=mode=='orphan',kill_failure=mode=='kill_failure')
            if expected:rejects(capture_process,proc,b'input',1,8,io,code=expected)
            else:
                assert capture_process(proc,b'input',1,8,io)==(0,b'12345',b'6')
                assert io.written==b'input';checks+=1
            assert proc.killed and all(x.closed for x in (proc.stdin,proc.stdout,proc.stderr))
        # Actual fixture_work and cleanup_owned below, with recorded transports
        # and fake owned storage. Only binary build/hash I/O is substituted.
        class State:
            def __init__(self):
                self.nonce=nonce;self.name='wt-authority-'+nonce;self.files={};self.removed=False
            def path(self,name=''):return '/private/'+name
            def verify(self):pass
            def record(self,name,data):
                assert name not in self.files
                self.files[name]=data
            def has(self,name):return name in self.files
            def read(self,name,limit):return self.files[name]
            def remove_confirmed(self):self.removed=True
        class RecordedDocker:
            def __init__(self,mode):
                self.mode=mode;self.calls=[];self.created=False;self.removed=False
            def call(self,args,**kwargs):
                self.calls.append(args)
                if args[:2]==['image','inspect']:
                    return 0,json.dumps({'Id':'sha256:'+'5'*64,'RepoDigests':[IMAGE]}).encode(),b''
                if args[0]=='run':
                    assert args.count('--pull=never') == 1
                    assert not any(x == '--pull' or x.startswith('--pull=') and x != '--pull=never' for x in args)
                    self.created=True
                    if self.mode=='uncertain_create':raise Failure('PROCESS_TIMEOUT')
                    return 0,('4'*64+'\n').encode(),b''
                if args[0]=='inspect':
                    v=synthetic_inspect(nonce)
                    if self.mode=='ownership_mismatch':v['Labels']={LABEL:'9'*32}
                    return 0,json.dumps(v).encode(),b''
                if args[:2]==['container','ls']:
                    if self.mode=='shutdown_unknown':raise Failure('PROCESS_FAILED')
                    return 0,(json.dumps({'Names':'wt-authority-'+nonce}).encode() if self.created and not self.removed else b''),b''
                if args[0]=='rm':
                    assert args==['rm','-f','4'*64]
                    if self.mode=='cleanup_failure':raise Failure('PROCESS_FAILED')
                    self.removed=True
                    return 0,b'',b''
                if args[0]=='cp':return 0,b'',b''
                if args[0]=='exec' and args[2]=='pg_isready':return 0,b'',b''
                if args[0]=='exec' and args[2]=='readlink':return 0,args[-1].encode()+b'\n',b''
                if args[:2]==['exec','-i']:
                    if self.mode in ('output_limit','deadline','signal_unknown'):
                        raise Failure({'output_limit':'PROCESS_OUTPUT_LIMIT','deadline':'PROCESS_TIMEOUT','signal_unknown':'PROCESS_UNCERTAIN'}[self.mode])
                    return (1 if self.mode=='test_exit' else 0),b'private-raw-secret',b'private-error-secret'
                raise AssertionError('unrecognized transport call')
            def checked(self,args,**kwargs):
                code,out,_=self.call(args,**kwargs)
                require(code==0,'PROCESS_FAILED')
                return out
        globals()['build_test_binary']=lambda state,go,budget:('/private/authority.test','3'*64)
        globals()['toolchain_identity']=lambda go:go
        for mode,expected in (('success',None),('cleanup_failure','CLEANUP_UNCONFIRMED'),
            ('uncertain_create','PROCESS_TIMEOUT'),('ownership_mismatch','CLEANUP_UNCONFIRMED'),
            ('shutdown_unknown','CLEANUP_UNCONFIRMED'),('output_limit','PROCESS_OUTPUT_LIMIT'),
            ('deadline','PROCESS_TIMEOUT'),('signal_unknown','PROCESS_UNCERTAIN'),
            ('test_exit','TEST_PROCESS_FAILED'),('converter_exit','CONVERTER_FAILED')):
            state=State();docker=RecordedDocker(mode)
            def convert(argv,**kwargs):
                assert argv[1:]==['tool','test2json','-p',PACKAGE]
                assert kwargs['data']==b'private-raw-secret'
                return (1 if mode=='converter_exit' else 0),encode(events),b'private-converter-secret'
            globals()['invoke']=convert
            operation=lambda:finalize_owned(lambda:fixture_work(state,docker,'/go',CASES,lambda x:x),
                                            lambda:cleanup_owned(state,docker,lambda x:x))
            if expected:
                rejects(operation,code=expected)
            else:
                report=operation();assert report['result']['leaves']==150
                assert 'secret' not in json.dumps(report);checks+=1
            assert any(c[:2]==['container','ls'] for c in docker.calls)
            if mode in ('cleanup_failure','ownership_mismatch','shutdown_unknown'):
                assert not state.removed
            else:
                assert state.removed
            if mode=='ownership_mismatch':assert not any(c[0]=='rm' for c in docker.calls)
        target=validate_target({
            'version':1,'run_nonce':nonce,'database':'wt_authority_test_'+nonce,'user':'wt_authority_owner_'+nonce,
            'password':'2'*64,'host':'127.0.0.1','port':5432,'schema_sha256':SCHEMA_SHA256,
            'binary_sha256':'3'*64,'image':IMAGE,'network_mode':'none','pid_mode':'','cpu_nanos':2000000000,
            'memory_bytes':1073741824,'memory_swap_bytes':1073741824,'shm_bytes':67108864,
            'tmpfs_bytes':2147483648,'no_mounts_or_ports':True,'run_owned':True})
        for key in target:
            bad=dict(target);del bad[key];rejects(validate_target,bad)
            bad=dict(target);bad[key]=None;rejects(validate_target,bad)
        for key in ('PGHOST','PGSERVICE','PGPASSFILE','TLSConfig','Fallbacks','options','DSN'):
            rejects(validate_target,dict(target,**{key:'secret-must-not-escape'}))
    finally:
        globals().update(old)
        for name,value in old_process.items():setattr(subprocess,name,value)
    print(json.dumps({'selftest':'PASS','checks':checks,'processes':0,'postgres':'NOT_RUN'},sort_keys=True))


def main(argv):
    args=parse_arguments(argv)
    if args[0]=='selftest':selftest()
    elif args[0]=='cleanup':print(cleanup(args[1],args[2]))
    else:run(args[1],args[2],secrets.token_hex(16),True,False)


if __name__=='__main__':
    os.umask(0o077)
    try:
        main(sys.argv[1:])
    except Failure as exc:
        print(json.dumps({'status':'FAIL','code':exc.code},separators=(',',':')))
        sys.exit(1)
    except BaseException:
        print('{"status":"FAIL","code":"UNEXPECTED_FAILURE"}')
        sys.exit(1)
