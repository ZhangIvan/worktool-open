#!/usr/bin/env python3
"""Inert-by-default, one-attempt synthetic registration acceptance supervisor.

Only explicit `run` or `cleanup` can use subprocesses. Offline selftests inject
an executor and never invoke Docker, PostgreSQL, Go or the network. This module
is not a production recovery authority and is not installed by an old harness.
"""
from __future__ import annotations

import argparse
from dataclasses import dataclass
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
import selectors
import shutil
import signal
import stat
import subprocess
import sys
import time

sys.dont_write_bytecode = True
IMAGE = 'postgres@sha256:41a369edc91d9bdd7a58be6bc71ea0f8f7a3f49e82818a9f0829021ea7840b7c'
IMAGE_DIGEST = IMAGE.split('@', 1)[1]
LABEL = 'io.worktool.registration.owner'
IDENTITY_LABEL = 'io.worktool.registration.identity'
DOCKER = '/usr/bin/docker'
SCHEMA = 'wt_synthetic_registry'
PACKAGE = 'github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg'
TEST = 'TestRegistrationAuthorityIsolatedPostgreSQL'
ROLE_NAMES = ('wt_registration_lab_owner', 'wt_registration_lab_issuer',
              'wt_registration_lab_consumer', 'wt_registration_lab_unrelated')
HEX = re.compile(r'[a-f0-9]{64}\Z')
ENV_ALLOW = 'one-reviewed-synthetic-pending-only-run-no-retry'
MAX_OUTPUT = 2 << 20
PROVISION_PHASES = ('bootstrap_preflight', 'bootstrap_roles', 'bootstrap_database', 'owner_schema') + tuple(
    'migration_%04d' % number for number in range(1, 11)) + ('registry', 'boundary', 'synthetic_seed', 'catalog_readback')
PROVISION_DIAGNOSTIC_CLASSES = frozenset(('SQL_PROCESS_PENDING', 'SQL_PROCESS_ACKNOWLEDGED',
    'SQL_PROCESS_FAILED', 'SQL_PROCESS_TIMEOUT', 'SQL_PROCESS_NONZERO', 'SQL_PROCESS_OUTPUT_LIMIT',
    'INTERRUPTED', 'JSON_DECODE_FAILED', 'JSON_SHAPE_INVALID', 'QUERY_RESULT_RETURNED',
    'UNEXPECTED_NONQUERY_OUTPUT', 'EMPTY_RESULT_ACKNOWLEDGED'))
PREFLIGHT_CHECKS = ('version', 'session_user', 'current_user', 'database_absent', 'roles_absent',
                    'other_clients', 'bootstrap_superuser', 'event_triggers')
PUBLIC_CODES = frozenset(('OK', 'NOT_RUN', 'HOSTED_CI_ONLY', 'INVALID_IDENTITY',
    'IDENTITY_MISMATCH', 'RUN_ATTEMPT_REFUSED', 'UNSAFE_PATH', 'UNSAFE_FILE',
    'SOURCE_PIN_MISMATCH', 'TOOLCHAIN_PIN_MISMATCH', 'INVALID_PIN',
    'RUN_ALREADY_STARTED', 'FOREIGN_CONTAINER_PRESENT', 'SUBPROCESS_NONZERO',
    'SUBPROCESS_FAILED', 'SUBPROCESS_TIMEOUT', 'OUTPUT_LIMIT', 'INTERRUPTED',
    'INVALID_OUTPUT', 'RESOURCE_MISMATCH', 'CONTAINER_IDENTITY_MISMATCH',
    'PG_READY_TIMEOUT', 'PG_SETTINGS_MISMATCH', 'PG_AUTHENTICATION_MISMATCH', 'PROVISION_FAILED',
    'BUILD_FAILED', 'EXECUTABLE_MISMATCH', 'EVIDENCE_REJECTED', 'RUN_FAILED',
    'CLEANUP_UNCONFIRMED', 'CLEANUP_IDENTITY_MISMATCH', 'CLEANUP_FILES_FAILED',
    'INTERNAL_FAILURE', 'MISSING_RUN_MARKER', 'INVALID_ARGUMENTS',
    'PATH_NOT_CANONICAL', 'PATH_NOT_DIRECTORY', 'PATH_OWNER_REFUSED', 'PATH_MODE_REFUSED',
    'TOOLCHAIN_STAGE_EXISTS', 'TOOLCHAIN_STAGE_CHANGED', 'TOOLCHAIN_STAGE_INVALID',
    'TOOLCHAIN_STAGE_TIMEOUT', 'TOOLCHAIN_STAGE_FAILED'))


def canonical(value):
    return json.dumps(value, separators=(',', ':'), ensure_ascii=True).encode()


def sha(body):
    return hashlib.sha256(body).hexdigest()


class Fault(RuntimeError):
    def __init__(self, code, uncertain=False):
        self.code = code if code in PUBLIC_CODES else 'INTERNAL_FAILURE'
        self.uncertain = uncertain
        self.partial_stdout = b''
        super().__init__(self.code)


def require(condition, code):
    if not condition:
        raise Fault(code)


def safe_code(error, default='INTERNAL_FAILURE'):
    return error.code if isinstance(error, Fault) else default


def load_helper(filename):
    path = Path(__file__).resolve().with_name(filename)
    name = filename.replace('-', '_').replace('.', '_')
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


@dataclass(frozen=True)
class Identity:
    repository: str
    commit: str
    ref: str
    run_id: str
    attempt: str

    @classmethod
    def checked(cls, repository, commit, ref, run_id, attempt, env):
        require(env.get('GITHUB_ACTIONS') == 'true' and
                env.get('RUNNER_ENVIRONMENT') == 'github-hosted' and
                env.get('RUNNER_OS') == 'Linux' and env.get('RUNNER_ARCH') == 'X64' and
                env.get('GITHUB_EVENT_NAME') == 'push' and
                env.get('GITHUB_WORKFLOW_REF') == 'ZhangIvan/worktool-open/.github/workflows/go-registration-acceptance.yml@refs/heads/worktool/synthetic-prototype', 'HOSTED_CI_ONLY')
        require(repository == 'ZhangIvan/worktool-open' and
                bool(re.fullmatch(r'[a-f0-9]{40}', commit)) and
                ref == 'refs/heads/worktool/synthetic-prototype' and
                bool(re.fullmatch(r'[1-9][0-9]{0,19}', run_id)), 'INVALID_IDENTITY')
        require(attempt == '1', 'RUN_ATTEMPT_REFUSED')
        expected = {'GITHUB_REPOSITORY': repository, 'GITHUB_SHA': commit,
                    'GITHUB_REF': ref, 'GITHUB_RUN_ID': run_id, 'GITHUB_RUN_ATTEMPT': attempt}
        require(all(env.get(k) == v for k, v in expected.items()), 'IDENTITY_MISMATCH')
        return cls(repository, commit, ref, run_id, attempt)

    def data(self):
        return dict(repository=self.repository, commit=self.commit, ref=self.ref,
                    run_id=self.run_id, attempt=self.attempt)

    @property
    def key(self):
        return sha(canonical(self.data()))

    @property
    def name(self):
        return 'wt-registration-' + self.run_id + '-' + self.attempt


def checked_directory(path, *, private=False, allow_root_owner=False):
    path = Path(path)
    require(path.is_absolute() and str(path) == os.path.normpath(str(path)), 'PATH_NOT_CANONICAL')
    require(path.resolve() == path, 'PATH_NOT_CANONICAL')
    s = path.lstat()
    require(stat.S_ISDIR(s.st_mode), 'PATH_NOT_DIRECTORY')
    require(s.st_uid in ((0, os.getuid()) if allow_root_owner and not private else (os.getuid(),)), 'PATH_OWNER_REFUSED')
    require(stat.S_IMODE(s.st_mode) == 0o700 if private else not s.st_mode & 0o022, 'PATH_MODE_REFUSED')
    return path


def read_regular(path, maximum, *, private=False):
    path = Path(path)
    require(path.is_absolute() and path.parent.resolve() == path.parent, 'UNSAFE_FILE')
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and not stat.S_ISLNK(info.st_mode) and
            0 < info.st_size <= maximum, 'UNSAFE_FILE')
    if private:
        checked_directory(path.parent, private=True)
        require(info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o600, 'UNSAFE_FILE')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as f:
        opened = os.fstat(f.fileno())
        require((info.st_dev, info.st_ino) == (opened.st_dev, opened.st_ino), 'UNSAFE_FILE')
        body = f.read(maximum + 1)
    require(len(body) <= maximum, 'UNSAFE_FILE')
    return body


def private_file(path, body):
    if isinstance(body, str):
        body = body.encode()
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'wb') as f:
        f.write(body)
        f.flush()
        os.fsync(f.fileno())


def replace_private(path, value):
    # Only known filenames inside an already authenticated private ledger.
    temporary = path.with_name(path.name + '.new')
    private_file(temporary, canonical(value))
    os.replace(temporary, path)


def _toolchain_input_root(root):
    # Read-only intake of an explicitly selected official setup-go cache. It is
    # NOT accepted as an execution/pin root here. Shared writable mode is allowed
    # only for byte-copy intake; all existing execution root checks are intact.
    root = Path(root)
    require(root.is_absolute() and str(root) == os.path.normpath(str(root)) and root.resolve() == root,
            'PATH_NOT_CANONICAL')
    info = root.lstat()
    require(stat.S_ISDIR(info.st_mode), 'PATH_NOT_DIRECTORY')
    require(info.st_uid in (0, os.getuid()), 'PATH_OWNER_REFUSED')
    return root


def _stage_fingerprint(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_uid, info.st_gid,
            info.st_nlink, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def _stage_read_file(path, deadline, expected=None, output=None):
    require(time.monotonic() < deadline, 'TOOLCHAIN_STAGE_TIMEOUT')
    before = path.lstat()
    require(stat.S_ISREG(before.st_mode) and not stat.S_ISLNK(before.st_mode) and before.st_nlink == 1 and
            before.st_uid in (0, os.getuid()) and not before.st_mode & 0o7000 and before.st_size <= 128 << 20,
            'TOOLCHAIN_STAGE_INVALID')
    fingerprint = _stage_fingerprint(before)
    if expected is not None:
        require(fingerprint == expected['metadata'], 'TOOLCHAIN_STAGE_CHANGED')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    digest, total = hashlib.sha256(), 0
    with os.fdopen(fd, 'rb') as stream:
        require(_stage_fingerprint(os.fstat(stream.fileno())) == fingerprint, 'TOOLCHAIN_STAGE_CHANGED')
        while True:
            require(time.monotonic() < deadline, 'TOOLCHAIN_STAGE_TIMEOUT')
            chunk = stream.read(1 << 20)
            if not chunk:
                break
            total += len(chunk)
            require(total <= 128 << 20 and total <= before.st_size, 'TOOLCHAIN_STAGE_INVALID')
            digest.update(chunk)
            if output is not None:
                output.write(chunk)
        require(_stage_fingerprint(os.fstat(stream.fileno())) == fingerprint and
                _stage_fingerprint(path.lstat()) == fingerprint and total == before.st_size, 'TOOLCHAIN_STAGE_CHANGED')
    result = {'sha256': digest.hexdigest(), 'size': total,
              'executable': bool(before.st_mode & 0o111), 'metadata': fingerprint}
    if expected is not None:
        require(result == expected, 'TOOLCHAIN_STAGE_CHANGED')
    return result


def _stage_inventory(root, deadline):
    root = _toolchain_input_root(root)
    root_info = _stage_fingerprint(root.lstat())
    files, directories, nodes, total = {}, {}, 0, 0
    def refuse_walk_error(_error):
        raise Fault('TOOLCHAIN_STAGE_INVALID')
    for parent, dirs, names in os.walk(root, followlinks=False, onerror=refuse_walk_error):
        for name in sorted(dirs + names):
            require(time.monotonic() < deadline, 'TOOLCHAIN_STAGE_TIMEOUT')
            nodes += 1
            path = Path(parent) / name
            rel = path.relative_to(root).as_posix()
            info = path.lstat()
            require(nodes <= 40000 and len(rel) <= 512 and not re.search(r'[\x00\r\n]', rel) and
                    name not in ('.git', 'node_modules', '__pycache__') and not stat.S_ISLNK(info.st_mode) and
                    info.st_uid in (0, os.getuid()), 'TOOLCHAIN_STAGE_INVALID')
            if name in dirs:
                require(stat.S_ISDIR(info.st_mode), 'TOOLCHAIN_STAGE_INVALID')
                directories[rel] = _stage_fingerprint(info)
            else:
                require(len(files) < 30000, 'TOOLCHAIN_STAGE_INVALID')
                record = _stage_read_file(path, deadline)
                total += record['size']
                require(total <= 512 << 20, 'TOOLCHAIN_STAGE_INVALID')
                files[rel] = record
    require(files and _stage_fingerprint(root.lstat()) == root_info, 'TOOLCHAIN_STAGE_CHANGED')
    return {'root': root_info, 'directories': dict(sorted(directories.items())), 'files': dict(sorted(files.items())), 'bytes': total}


def stage_toolchain(root, destination):
    """Bounded private byte copy only. No execution, permission repair or retry.

    Official provenance remains the pinned setup-go/download step. Snapshot
    comparison detects ordinary changes; it is not hostile-host attestation.
    Existing cache paths and modes are never changed. A partial failed output
    remains non-adoptable and unpinned; subsequent invocation refuses it.
    """
    deadline = time.monotonic() + 60
    root = _toolchain_input_root(root)
    destination = Path(destination)
    require(destination.is_absolute() and str(destination) == os.path.normpath(str(destination)) and
            destination.parent.resolve() == destination.parent and not destination.is_relative_to(root), 'PATH_NOT_CANONICAL')
    checked_directory(destination.parent, private=True)
    require(not destination.exists() and not destination.is_symlink(), 'TOOLCHAIN_STAGE_EXISTS')
    before = _stage_inventory(root, deadline)
    executables = ('bin/go', 'pkg/tool/linux_amd64/compile', 'pkg/tool/linux_amd64/link',
                   'pkg/tool/linux_amd64/asm', 'pkg/tool/linux_amd64/vet')
    required = ('VERSION', 'src/cmd/test2json/main.go') + executables
    require(all(name in before['files'] for name in required) and
            read_regular(root / 'VERSION', 4096).splitlines()[0] == b'go1.27.3' and
            all(before['files'][name]['executable'] for name in executables), 'TOOLCHAIN_STAGE_INVALID')
    destination.mkdir(mode=0o700)
    for rel in sorted(before['directories'], key=lambda value: (len(Path(value).parts), value)):
        require(time.monotonic() < deadline, 'TOOLCHAIN_STAGE_TIMEOUT')
        (destination / rel).mkdir(mode=0o700)
    for rel, record in before['files'].items():
        target = destination / rel
        mode = 0o700 if record['executable'] else 0o600
        fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
        with os.fdopen(fd, 'wb') as output:
            _stage_read_file(root / rel, deadline, expected=record, output=output)
        info = target.lstat()
        require(stat.S_IMODE(info.st_mode) == mode and info.st_uid == os.getuid() and info.st_nlink == 1,
                'TOOLCHAIN_STAGE_INVALID')
    after = _stage_inventory(root, deadline)
    require(after == before, 'TOOLCHAIN_STAGE_CHANGED')
    copied = _stage_inventory(destination, deadline)
    require(set(copied['directories']) == set(before['directories']) and set(copied['files']) == set(before['files']) and
            copied['bytes'] == before['bytes'] and
            all(all(copied['files'][name][key] == record[key] for key in ('sha256', 'size', 'executable'))
                for name, record in before['files'].items()), 'TOOLCHAIN_STAGE_CHANGED')
    checked_directory(destination, private=True)
    require(time.monotonic() < deadline, 'TOOLCHAIN_STAGE_TIMEOUT')
    hashes = {name: record['sha256'] for name, record in before['files'].items()}
    return {'status': 'TOOLCHAIN_STAGED_NOT_EXECUTED', 'tree_sha256': tree_digest(hashes),
            'file_count': len(hashes), 'bytes': before['bytes'], 'input': 'READ_ONLY_COPY_SOURCE',
            'output': 'PRIVATE_OWNED_NO_SYMLINKS', 'provenance': 'PINNED_SETUP_GO_REQUIRED'}


def tree_files(root, *, toolchain=False):
    root = checked_directory(root, allow_root_owner=toolchain)
    files, total, nodes = {}, 0, 0
    max_files, max_nodes = (30000, 40000) if toolchain else (1024, 2048)
    max_file, max_total = (128 << 20, 512 << 20) if toolchain else (1 << 20, 16 << 20)
    for parent, dirs, names in os.walk(root, followlinks=False):
        for name in dirs + names:
            nodes += 1
            path = Path(parent) / name
            rel = path.relative_to(root).as_posix()
            require(nodes <= max_nodes and len(rel) <= 512 and not re.search(r'[\x00\r\n]', rel), 'INVALID_PIN')
            info = path.lstat()
            require(not stat.S_ISLNK(info.st_mode), 'INVALID_PIN')
            if name in dirs:
                require(stat.S_ISDIR(info.st_mode) and name not in ('.git', 'node_modules', '__pycache__'), 'INVALID_PIN')
                continue
            require(stat.S_ISREG(info.st_mode) and info.st_size <= max_file, 'INVALID_PIN')
            total += info.st_size
            require(total <= max_total and len(files) < max_files, 'INVALID_PIN')
            files[rel] = sha(path.read_bytes())
    require(files, 'INVALID_PIN')
    return dict(sorted(files.items()))


def tree_digest(files):
    return sha(b''.join((path + '\0' + digest + '\n').encode() for path, digest in sorted(files.items())))


def make_pin(root, *, toolchain=False):
    files = tree_files(root, toolchain=toolchain)
    kind = 'toolchain' if toolchain else 'source'
    return canonical(dict(format='worktool-registration-' + kind + '-pin-v1',
                          tree_sha256=tree_digest(files), files=files))


def verify_pin(root, pin_path, pin_sha256, *, toolchain=False):
    require(bool(HEX.fullmatch(pin_sha256)), 'INVALID_PIN')
    raw = read_regular(pin_path, 8 << 20)
    require(sha(raw) == pin_sha256, 'INVALID_PIN')
    try:
        pin = json.loads(raw)
    except (ValueError, TypeError):
        raise Fault('INVALID_PIN') from None
    kind = 'toolchain' if toolchain else 'source'
    require(isinstance(pin, dict) and set(pin) == {'format', 'tree_sha256', 'files'} and
            pin['format'] == 'worktool-registration-' + kind + '-pin-v1' and
            isinstance(pin['files'], dict) and canonical(pin) == raw, 'INVALID_PIN')
    actual = tree_files(root, toolchain=toolchain)
    require(actual == pin['files'] and tree_digest(actual) == pin['tree_sha256'],
            'TOOLCHAIN_PIN_MISMATCH' if toolchain else 'SOURCE_PIN_MISMATCH')
    return pin['tree_sha256']


@dataclass
class Result:
    returncode: int
    stdout: bytes = b''
    stderr: bytes = b''


class Executor:
    """Bounded binary pipes, sanitized exceptions, kill process group on fault."""
    def call(self, args, *, data=b'', env=None, cwd=None, timeout=30):
        require(isinstance(args, (list, tuple)) and 1 <= len(args) <= 100 and
                all(isinstance(a, str) and 0 < len(a) <= 8192 and '\x00' not in a for a in args) and
                0 < timeout <= 300 and len(data) <= 2 << 20, 'INVALID_ARGUMENTS')
        environment = {'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8', 'LC_ALL': 'C.UTF-8'}
        if env:
            environment.update(env)
        p = None
        try:
            p = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                 stderr=subprocess.PIPE, env=environment, cwd=cwd,
                                 start_new_session=True, close_fds=True, umask=0o077)
            selector = selectors.DefaultSelector()
            for stream in (p.stdin, p.stdout, p.stderr):
                os.set_blocking(stream.fileno(), False)
            if data:
                selector.register(p.stdin, selectors.EVENT_WRITE, 'input')
            else:
                p.stdin.close()
            selector.register(p.stdout, selectors.EVENT_READ, 'stdout')
            selector.register(p.stderr, selectors.EVENT_READ, 'stderr')
            buffers = {'stdout': bytearray(), 'stderr': bytearray()}
            offset, deadline = 0, time.monotonic() + timeout
            while selector.get_map():
                require(time.monotonic() < deadline, 'SUBPROCESS_TIMEOUT')
                for key, _ in selector.select(min(.1, max(0, deadline - time.monotonic()))):
                    if key.data == 'input':
                        try:
                            offset += os.write(key.fd, data[offset:offset + 65536])
                        except BrokenPipeError:
                            offset = len(data)
                        if offset == len(data):
                            selector.unregister(key.fileobj)
                            key.fileobj.close()
                    else:
                        chunk = os.read(key.fd, 65536)
                        if not chunk:
                            selector.unregister(key.fileobj)
                            key.fileobj.close()
                        else:
                            buffers[key.data].extend(chunk)
                            require(sum(map(len, buffers.values())) <= MAX_OUTPUT, 'OUTPUT_LIMIT')
            p.wait(timeout=max(.01, deadline - time.monotonic()))
            return Result(p.returncode, bytes(buffers['stdout']), bytes(buffers['stderr']))
        except BaseException as error:
            if p is not None:
                try:
                    os.killpg(p.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                try:
                    p.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    pass
            if isinstance(error, Fault):
                error.uncertain = True
                if 'buffers' in locals():
                    error.partial_stdout = bytes(buffers['stdout'][:MAX_OUTPUT])
                raise
            if isinstance(error, KeyboardInterrupt):
                raise Fault('INTERRUPTED', True) from None
            raise Fault('SUBPROCESS_FAILED', True) from None
        finally:
            if 'selector' in locals():
                selector.close()
            if p is not None:
                for stream in (p.stdin, p.stdout, p.stderr):
                    if stream and not stream.closed:
                        stream.close()


# This is deliberately a whitelist: Config.Env and raw Docker inspect never
# enter process output, a log, retained evidence, or an exception.
INSPECT_TEMPLATE = ('{"id":{{json .Id}},"name":{{json .Name}},'
    '"owner":{{json (index .Config.Labels "' + LABEL + '")}},'
    '"identity":{{json (index .Config.Labels "' + IDENTITY_LABEL + '")}},'
    '"image":{{json .Config.Image}},"image_id":{{json .Image}},'
    '"mounts":{{json .Mounts}},"tmpfs":{{json .HostConfig.Tmpfs}},'
    '"memory":{{.HostConfig.Memory}},"swap":{{.HostConfig.MemorySwap}},'
    '"cpus":{{.HostConfig.NanoCpus}},"shm":{{.HostConfig.ShmSize}},'
    '"privileged":{{.HostConfig.Privileged}},"network":{{json .HostConfig.NetworkMode}},'
    '"pid":{{json .HostConfig.PidMode}},"ipc":{{json .HostConfig.IpcMode}},'
    '"cap_add":{{json .HostConfig.CapAdd}},"cap_drop":{{json .HostConfig.CapDrop}},'
    '"ports":{{json .NetworkSettings.Ports}},"running":{{.State.Running}}}')


def parse_json(raw):
    require(len(raw) <= MAX_OUTPUT, 'INVALID_OUTPUT')
    try:
        return json.loads(raw)
    except (ValueError, TypeError):
        raise Fault('INVALID_OUTPUT') from None


def verify_container(v, identity, owner, *, resources=True, container_id=None):
    require(isinstance(v, dict) and v.get('name') == '/' + identity.name and
            v.get('owner') == owner and v.get('identity') == identity.key and
            bool(HEX.fullmatch(v.get('id', ''))) and v.get('image') == IMAGE and
            bool(re.fullmatch(r'sha256:[a-f0-9]{64}', v.get('image_id', ''))), 'CONTAINER_IDENTITY_MISMATCH')
    if container_id:
        require(v['id'] == container_id, 'CONTAINER_IDENTITY_MISMATCH')
    if not resources:
        return None
    require(v.get('memory') == 1 << 30 and v.get('swap') == 1 << 30 and
            v.get('cpus') == 2_000_000_000 and v.get('shm') == 64 << 20 and
            v.get('privileged') is False and v.get('network') == 'bridge' and
            v.get('pid') == '' and v.get('ipc') == 'private' and not v.get('cap_add') and
            v.get('cap_drop') in (['NET_RAW'], ['CAP_NET_RAW']) and v.get('running') is True, 'RESOURCE_MISMATCH')
    require(v.get('tmpfs') == {'/var/lib/postgresql': 'rw,nosuid,nodev,size=2147483648'} and
            isinstance(v.get('mounts'), list) and all(m.get('Type') == 'tmpfs' and
                m.get('Destination') == '/var/lib/postgresql' for m in v['mounts']), 'RESOURCE_MISMATCH')
    ports = v.get('ports', {})
    require(set(ports) == {'5432/tcp'} and isinstance(ports['5432/tcp'], list) and len(ports['5432/tcp']) == 1,
            'RESOURCE_MISMATCH')
    port = ports['5432/tcp'][0]
    require(port.get('HostIp') == '127.0.0.1' and bool(re.fullmatch('[0-9]{4,5}', port.get('HostPort', ''))) and
            1024 <= int(port['HostPort']) <= 65535, 'RESOURCE_MISMATCH')
    return int(port['HostPort'])


def container_diagnostics(view, identity, owner, container_id, image_id, expected_port=None):
    """Fixed categories only, never observed values or acceptance authority.

    Observe the existing whitelist response without additional Docker calls.
    Each check is independent, so an early mismatch cannot hide later ones.
    The verifier remains authoritative, including its malformed-input behavior.
    """
    def observe(check):
        try:
            return 'MATCH' if check() else 'MISMATCH'
        except (AttributeError, IndexError, KeyError, TypeError, ValueError, OverflowError):
            return 'INVALID_SHAPE'

    def identity_matches():
        try:
            verify_container(view, identity, owner, resources=False, container_id=container_id)
            return True
        except Fault:
            return False

    checks = {
        'owned_identity': observe(identity_matches),
        'image_id_matches': observe(lambda: view.get('image_id') == image_id),
        'memory_limit': observe(lambda: view.get('memory') == 1 << 30),
        'memory_swap_limit': observe(lambda: view.get('swap') == 1 << 30),
        'cpu_limit': observe(lambda: view.get('cpus') == 2_000_000_000),
        'shm_limit': observe(lambda: view.get('shm') == 64 << 20),
        'unprivileged': observe(lambda: view.get('privileged') is False),
        'bridge_network': observe(lambda: view.get('network') == 'bridge'),
        'private_pid': observe(lambda: view.get('pid') == ''),
        'private_ipc': observe(lambda: view.get('ipc') == 'private'),
        'no_added_capabilities': observe(lambda: not view.get('cap_add')),
        'drop_net_raw_literal': observe(lambda: view.get('cap_drop') == ['NET_RAW']),
        'drop_net_raw_canonical': observe(lambda: view.get('cap_drop') == ['CAP_NET_RAW']),
        'drop_net_raw_only': observe(lambda: view.get('cap_drop') in (['NET_RAW'], ['CAP_NET_RAW'])),
        'running': observe(lambda: view.get('running') is True),
        'tmpfs_exact': observe(lambda: view.get('tmpfs') == {'/var/lib/postgresql': 'rw,nosuid,nodev,size=2147483648'}),
        'mounts_tmpfs_only': observe(lambda: isinstance(view.get('mounts'), list) and all(
            mount.get('Type') == 'tmpfs' and mount.get('Destination') == '/var/lib/postgresql' for mount in view['mounts'])),
        'port_keys': observe(lambda: set(view.get('ports', {})) == {'5432/tcp'}),
        'port_single_binding': observe(lambda: isinstance(view['ports']['5432/tcp'], list) and len(view['ports']['5432/tcp']) == 1),
        'port_loopback': observe(lambda: view['ports']['5432/tcp'][0].get('HostIp') == '127.0.0.1'),
        'port_range': observe(lambda: bool(re.fullmatch('[0-9]{4,5}', view['ports']['5432/tcp'][0].get('HostPort', ''))) and
            1024 <= int(view['ports']['5432/tcp'][0]['HostPort']) <= 65535),
        'port_unchanged': 'NOT_APPLICABLE' if expected_port is None else observe(
            lambda: int(view['ports']['5432/tcp'][0]['HostPort']) == expected_port),
    }
    return {'format': 'worktool-registration-container-diagnostics-v1', 'authority': 'DIAGNOSTIC_ONLY', 'checks': checks}


HBA_PATH = '/var/lib/postgresql/18/docker/pg_hba.conf'
HBA_QUERY = """SELECT pg_catalog.json_build_object(
 'session_user',session_user,'current_user',current_user,'database',pg_catalog.current_database(),
 'hba_file',pg_catalog.current_setting('hba_file'),
 'config_load_epoch_us',(extract(epoch FROM pg_catalog.pg_conf_load_time())*1000000)::bigint,
 'postmaster_start_epoch_us',(extract(epoch FROM pg_catalog.pg_postmaster_start_time())*1000000)::bigint,
 'checked_at_epoch_us',(extract(epoch FROM pg_catalog.clock_timestamp())*1000000)::bigint,
 'file_mtime_epoch_us',(extract(epoch FROM (pg_catalog.pg_stat_file(pg_catalog.current_setting('hba_file'))).modification)*1000000)::bigint,
 'file_size',(pg_catalog.pg_stat_file(pg_catalog.current_setting('hba_file'))).size,
 'file_is_directory',(pg_catalog.pg_stat_file(pg_catalog.current_setting('hba_file'))).isdir,
 'rule_count',(SELECT count(*) FROM pg_catalog.pg_hba_file_rules),
 'rules',(SELECT pg_catalog.json_agg(pg_catalog.json_build_object(
   'rule_number',rule_number,'file_name',file_name,'line_number',line_number,'type',type,
   'database',database,'users',user_name,'address',address,'netmask',netmask,
   'auth_method',auth_method,'options',options,'error',error) ORDER BY rule_number)
   FROM (SELECT * FROM pg_catalog.pg_hba_file_rules ORDER BY rule_number LIMIT 33) AS bounded_rules)
)::text;"""


def verify_hba_snapshot(value, admin):
    """Validate file-view facts, not an independently exposed loaded HBA cache."""
    keys = {'session_user', 'current_user', 'database', 'hba_file', 'config_load_epoch_us',
            'postmaster_start_epoch_us', 'checked_at_epoch_us', 'file_mtime_epoch_us',
            'file_size', 'file_is_directory', 'rule_count', 'rules'}
    require(isinstance(value, dict) and set(value) == keys and value['session_user'] == admin and
            value['current_user'] == admin and value['database'] == 'postgres' and
            value['hba_file'] == HBA_PATH and value['file_is_directory'] is False and
            type(value['file_size']) is int and 0 < value['file_size'] <= 32768, 'PG_AUTHENTICATION_MISMATCH')
    time_keys = ('file_mtime_epoch_us', 'postmaster_start_epoch_us', 'config_load_epoch_us', 'checked_at_epoch_us')
    require(all(type(value[k]) is int and 1577836800000000 <= value[k] <= 4102444800000000 for k in time_keys) and
            value['file_mtime_epoch_us'] <= value['config_load_epoch_us'] <= value['checked_at_epoch_us'] and
            value['postmaster_start_epoch_us'] <= value['checked_at_epoch_us'], 'PG_AUTHENTICATION_MISMATCH')
    require(type(value['rule_count']) is int and 1 <= value['rule_count'] <= 32 and
            isinstance(value['rules'], list) and len(value['rules']) == value['rule_count'], 'PG_AUTHENTICATION_MISMATCH')
    rule_keys = {'rule_number', 'file_name', 'line_number', 'type', 'database', 'users', 'address', 'netmask', 'auth_method', 'options', 'error'}
    numbers, lines, host_rules, catchall = [], [], [], False
    for rule in value['rules']:
        require(isinstance(rule, dict) and set(rule) == rule_keys and rule['file_name'] == HBA_PATH and
                type(rule['rule_number']) is int and 1 <= rule['rule_number'] <= 32 and
                type(rule['line_number']) is int and 1 <= rule['line_number'] <= 4096 and
                rule['type'] in ('local', 'host', 'hostssl', 'hostnossl') and
                rule['database'] in (['all'], ['replication']) and rule['users'] == ['all'] and
                rule['options'] in (None, []) and rule['error'] is None, 'PG_AUTHENTICATION_MISMATCH')
        numbers.append(rule['rule_number'])
        lines.append(rule['line_number'])
        if rule['type'] == 'local':
            require(rule['address'] is None and rule['netmask'] is None and rule['auth_method'] == 'trust',
                    'PG_AUTHENTICATION_MISMATCH')
        else:
            require(rule['auth_method'] == 'scram-sha-256' and (rule['address'], rule['netmask']) in (
                ('127.0.0.1', '255.255.255.255'), ('::1', 'ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff'), ('all', None)),
                'PG_AUTHENTICATION_MISMATCH')
            host_rules.append({k: rule[k] for k in ('type', 'database', 'users', 'address', 'netmask', 'auth_method')})
            catchall |= rule['type'] == 'host' and rule['database'] == ['all'] and rule['address'] == 'all'
    require(numbers == list(range(1, value['rule_count'] + 1)) and lines == sorted(set(lines)) and host_rules and catchall,
            'PG_AUTHENTICATION_MISMATCH')
    fixed = {k: v for k, v in value.items() if k != 'checked_at_epoch_us'}
    return fixed, host_rules


@dataclass
class RunConfig:
    identity: Identity
    runner_temp: Path
    source_root: Path
    source_pin: Path
    source_pin_sha256: str
    go_binary: Path
    toolchain_pin: Path
    toolchain_pin_sha256: str
    module_cache: Path
    job_deadline_ns: int = 0


class Supervisor:
    def __init__(self, config, executor=None, provision_helper=None, evidence_helper=None,
                 sleep=time.sleep, random_hex=secrets.token_hex, monotonic=time.monotonic):
        self.config = config
        self.identity = config.identity
        self.executor = executor if executor is not None else Executor()
        self.provision_helper = provision_helper
        self.evidence_helper = evidence_helper
        self.sleep, self.random_hex, self.monotonic = sleep, random_hex, monotonic
        self.operation_deadline = None
        self.container_verified = False
        self.resource_diagnostics = {}
        self.provision_diagnostics = {}
        self.preflight_diagnostics = None
        self.server_settings_verified = False
        self.folder = Path(config.runner_temp) / self.identity.name
        self.marker = None
        self.stage = 'preflight'
        self.credentials = {}
        self.acceptance_events = b''
        self.binding = None
        self.started = False
        self.interrupted = False
        self.old_signals = {}

    def call(self, args, *, check=True, **kwargs):
        if self.operation_deadline is not None:
            remaining = self.operation_deadline - self.monotonic()
            require(remaining > 0, 'SUBPROCESS_TIMEOUT')
            kwargs['timeout'] = min(kwargs.get('timeout', 30), remaining)
        result = self.executor.call(args, **kwargs)
        require(isinstance(result, Result) and len(result.stdout) + len(result.stderr) <= MAX_OUTPUT, 'INVALID_OUTPUT')
        if check:
            require(result.returncode == 0, 'SUBPROCESS_NONZERO')
        return result

    def existing_ids(self):
        result = self.call([DOCKER, 'ps', '-aq', '--no-trunc', '--filter', 'name=^/' + self.identity.name + '$'])
        ids = result.stdout.decode('ascii').strip().splitlines()
        require(len(ids) <= 1 and all(HEX.fullmatch(x) for x in ids), 'INVALID_OUTPUT')
        return ids

    def inspect(self):
        return parse_json(self.call([DOCKER, 'inspect', '--format', INSPECT_TEMPLATE, self.identity.name]).stdout)

    def save_marker(self):
        replace_private(self.folder / 'run-marker.json', self.marker)

    def preflight(self):
        require(Path(__file__).resolve().parents[1] == self.config.source_root, 'SOURCE_PIN_MISMATCH')
        checked_directory(self.config.runner_temp)
        require(not self.folder.exists() and not self.folder.is_symlink(), 'RUN_ALREADY_STARTED')
        source = verify_pin(self.config.source_root, self.config.source_pin, self.config.source_pin_sha256)
        go_binary = Path(self.config.go_binary)
        require(go_binary.name == 'go' and go_binary.parent.name == 'bin' and go_binary.resolve() == go_binary,
                'UNSAFE_PATH')
        self.toolchain_root = checked_directory(go_binary.parent.parent, allow_root_owner=True)
        require(read_regular(self.toolchain_root / 'VERSION', 4096).splitlines()[0] == b'go1.27.3', 'TOOLCHAIN_PIN_MISMATCH')
        self.toolchain_sha256 = verify_pin(self.toolchain_root, self.config.toolchain_pin, self.config.toolchain_pin_sha256, toolchain=True)
        checked_directory(self.config.module_cache)
        self.provision_helper = self.provision_helper or load_helper('pg-registration-provision.py')
        self.evidence_helper = self.evidence_helper or load_helper('pg-registration-evidence.py')
        self.bundle = self.provision_helper.load_and_verify_sources(self.config.source_root)
        self.cases = self.evidence_helper.load_manifest(self.config.source_root / 'scripts/pg-registration-cases.json')
        require(self.evidence_helper.source_digest(self.config.source_root, self.cases) == source, 'SOURCE_PIN_MISMATCH')
        self.source_sha256 = source
        require(not self.existing_ids(), 'FOREIGN_CONTAINER_PRESENT')
        self.folder.mkdir(mode=0o700)
        self.marker = {'format': 'worktool-registration-owned-run-v1', 'identity': self.identity.data(),
                       'owner': self.random_hex(32), 'container_name': self.identity.name,
                       'container_id': '', 'create_state': 'NOT_SENT', 'run_invocations': 0,
                       'source_sha256': source, 'state': 'STARTED', 'job_deadline_ns': self.config.job_deadline_ns}
        require(bool(HEX.fullmatch(self.marker['owner'])), 'INVALID_ARGUMENTS')
        private_file(self.folder / 'run-marker.json', canonical(self.marker))
        self.started = True

    def install_signals(self):
        def interrupted(signum, frame):
            self.interrupted = True
            raise Fault('INTERRUPTED', True)
        for sig in (signal.SIGINT, signal.SIGTERM):
            self.old_signals[sig] = signal.signal(sig, interrupted)

    def restore_signals(self):
        for sig, handler in self.old_signals.items():
            signal.signal(sig, handler)

    def create_container(self):
        self.stage = 'create'
        suffix = self.random_hex(16)
        require(bool(re.fullmatch('[a-f0-9]{32}', suffix)), 'INVALID_ARGUMENTS')
        self.database = 'wt_test_' + suffix
        self.admin = 'wt_admin_' + suffix
        self.run_id = 'synthetic_' + suffix
        for role in (self.admin,) + ROLE_NAMES:
            self.credentials[role] = self.random_hex(32)
        require(len(set(self.credentials.values())) == 5 and all(HEX.fullmatch(v) for v in self.credentials.values()),
                'INVALID_ARGUMENTS')
        # POSTGRES_DB stays postgres. The helper creates the one random test DB
        # explicitly from the newly initialized disposable cluster, not adoption.
        private_file(self.folder / 'container.env', 'POSTGRES_USER=' + self.admin + '\nPOSTGRES_PASSWORD=' +
                     self.credentials[self.admin] + '\nPOSTGRES_DB=postgres\nPOSTGRES_INITDB_ARGS=--auth-host=scram-sha-256\n'
                     'PGDATA=/var/lib/postgresql/18/docker\n')
        self.call([DOCKER, 'pull', '--platform', 'linux/amd64', IMAGE], timeout=180)
        image = parse_json(self.call([DOCKER, 'image', 'inspect', '--format',
                                     '{"id":{{json .Id}},"digests":{{json .RepoDigests}},"architecture":{{json .Architecture}},"os":{{json .Os}}}', IMAGE]).stdout)
        require(image.get('architecture') == 'amd64' and image.get('os') == 'linux' and
                IMAGE in image.get('digests', []) and bool(re.fullmatch(r'sha256:[a-f0-9]{64}', image.get('id', ''))), 'RESOURCE_MISMATCH')
        self.image_id = image['id']
        require(self.operation_deadline - self.monotonic() >= 90, 'SUBPROCESS_TIMEOUT')
        self.marker['create_state'] = 'SENT'
        self.save_marker()
        result = self.call([DOCKER, 'create', '--name', self.identity.name,
            '--label', LABEL + '=' + self.marker['owner'], '--label', IDENTITY_LABEL + '=' + self.identity.key,
            '--platform', 'linux/amd64', '--network', 'bridge', '--ipc', 'private', '--cap-drop', 'NET_RAW',
            '--cpus', '2', '--memory', '1g', '--memory-swap', '1g', '--shm-size', '64m',
            '--tmpfs', '/var/lib/postgresql:rw,nosuid,nodev,size=2147483648',
            '--publish', '127.0.0.1::5432', '--env-file', str(self.folder / 'container.env'), IMAGE,
            'postgres', '-c', 'max_connections=10', '-c', 'superuser_reserved_connections=3',
            '-c', 'reserved_connections=0', '-c', 'lc_messages=C', '-c', 'log_statement=none', '-c', 'log_min_error_statement=panic',
            '-c', 'statement_timeout=5s', '-c', 'lock_timeout=1s', '-c', 'idle_in_transaction_session_timeout=5s'], timeout=60)
        cid = result.stdout.decode('ascii').strip()
        require(bool(HEX.fullmatch(cid)), 'INVALID_OUTPUT')
        self.marker['container_id'] = cid
        self.marker['create_state'] = 'ACKNOWLEDGED'
        self.save_marker()
        self.call([DOCKER, 'start', cid], timeout=30)
        view = self.inspect()
        self.resource_diagnostics['post_start'] = container_diagnostics(
            view, self.identity, self.marker['owner'], cid, self.image_id)
        self.port = verify_container(view, self.identity, self.marker['owner'], container_id=cid)
        require(view['image_id'] == self.image_id, 'RESOURCE_MISMATCH')
        self.container_verified = True
        self.container_observation = {key: view[key] for key in ('id', 'image', 'image_id', 'memory', 'swap', 'cpus', 'shm', 'tmpfs', 'network', 'pid', 'ipc', 'privileged', 'ports')}

    def sql_process(self, role, database, sql, *, timeout=30, password=None, check=True):
        require(role in self.credentials and database in ('postgres', self.database) and isinstance(sql, str) and
                0 < len(sql.encode()) <= 1 << 20, 'INVALID_ARGUMENTS')
        # Constant shell command; secrets and SQL go only to stdin. Actual TCP
        # authentication occurs as the requested role. No SET ROLE shortcut.
        password = self.credentials[role] if password is None else password
        require(isinstance(password, str) and bool(HEX.fullmatch(password)), 'INVALID_ARGUMENTS')
        script = 'IFS= read -r PGPASSWORD; LC_ALL=C; LANG=C; PGSSLMODE=disable; export PGPASSWORD LC_ALL LANG PGSSLMODE; exec psql -w -X -q -A -t -P pager=off -v ON_ERROR_STOP=1 -h 127.0.0.1 -p 5432 -U "$1" -d "$2"'
        return self.call([DOCKER, 'exec', '-i', self.marker['container_id'], 'sh', '-c', script,
                          'registration-psql', role, database],
                         data=(password + '\n' + sql + '\n').encode(), timeout=timeout, check=check)

    def sql_call(self, role, database, sql, *, timeout=30):
        return self.sql_process(role, database, sql, timeout=timeout).stdout.strip()

    def verify_authentication(self):
        self.authentication_summary = {'status': 'NOT_VERIFIED', 'hba_rules': 'NOT_VERIFIED',
            'wrong_password_probe': 'NOT_RUN', 'loaded_hba_cache': 'NOT_DIRECTLY_EXPOSED_BY_POSTGRESQL'}
        self.stage = 'auth_hba_path'
        canonical_hba = self.call([DOCKER, 'exec', self.marker['container_id'], 'readlink', '-f', HBA_PATH], timeout=10).stdout.strip()
        require(canonical_hba == HBA_PATH.encode(), 'PG_AUTHENTICATION_MISMATCH')
        self.stage = 'auth_hba_before'
        before, host_rules = verify_hba_snapshot(parse_json(self.sql_call(self.admin, 'postgres', HBA_QUERY)), self.admin)
        self.authentication_summary.update(hba_rules='FILE_RULES_VERIFIED', hba_file=HBA_PATH,
            host_rules=host_rules, local_socket_rules=[{k: rule[k] for k in ('type', 'database', 'users', 'auth_method')} for rule in before['rules'] if rule['type'] == 'local'],
            successful_admin_identity_reads=1, rule_count=before['rule_count'], hba_file_size=before['file_size'],
            hba_file_mtime_epoch_us=before['file_mtime_epoch_us'], postmaster_start_epoch_us=before['postmaster_start_epoch_us'],
            config_load_epoch_us=before['config_load_epoch_us'])
        self.stage = 'auth_wrong_password'
        wrong = self.random_hex(32)
        require(bool(HEX.fullmatch(wrong)) and wrong not in self.credentials.values(), 'INVALID_ARGUMENTS')
        self.authentication_summary['wrong_password_probe'] = 'ATTEMPTED_OUTCOME_UNCONFIRMED'
        rejected = self.sql_process(self.admin, 'postgres', 'SELECT 1;', password=wrong, timeout=10, check=False)
        expected = ('psql: error: connection to server at "127.0.0.1", port 5432 failed: FATAL:  password authentication failed for user "' + self.admin + '"').encode()
        self.authentication_summary['wrong_password_probe'] = 'UNCLASSIFIED_FAILURE' if rejected.returncode else 'UNEXPECTED_ACCEPTANCE'
        require(rejected.returncode == 2 and rejected.stdout == b'' and rejected.stderr.strip() == expected, 'PG_AUTHENTICATION_MISMATCH')
        self.authentication_summary['wrong_password_probe'] = 'AUTH_DENIAL_OBSERVED'
        self.stage = 'auth_hba_after'
        after, after_rules = verify_hba_snapshot(parse_json(self.sql_call(self.admin, 'postgres', HBA_QUERY)), self.admin)
        require(after == before and after_rules == host_rules, 'PG_AUTHENTICATION_MISMATCH')
        self.authentication_summary.update(status='VERIFIED_IN_ISOLATED_SCOPE',
            hba_snapshot_sha256=sha(canonical(before)), successful_admin_identity_reads=2,
            proof_model='FILE_RULES_PLUS_TCP_PASSWORD_DENIAL_TRUSTED_NEW_SERVER_LIFECYCLE')

    def record_execution_diagnostic(self, phase, classification):
        # This sink is observational only. Never copy arbitrary phase names or
        # exception text, and never let a diagnostic failure alter execution.
        if type(phase) is str and phase in PROVISION_PHASES and type(classification) is str and classification in PROVISION_DIAGNOSTIC_CLASSES:
            self.provision_diagnostics[phase] = classification

    def record_preflight_diagnostics(self, diagnostic):
        # Independently validate the helper's entire small schema. Invalid
        # diagnostic payloads are ignored; they cannot authorize or reject SQL.
        try:
            if type(diagnostic) is not dict or set(diagnostic) != {'format', 'authority', 'classification', 'shape', 'checks'}:
                return
            if diagnostic['format'] != 'worktool-registration-preflight-fields-v1' or diagnostic['authority'] != 'DIAGNOSTIC_ONLY':
                return
            if type(diagnostic['classification']) is not str or diagnostic['classification'] not in ('VALUE_MATCH', 'VALUE_MISMATCH', 'INVALID_SHAPE'):
                return
            shape, checks = diagnostic['shape'], diagnostic['checks']
            if type(shape) is not dict or set(shape) != {'root', 'row_count', 'row', 'fields'} or type(checks) is not dict or set(checks) != set(PREFLIGHT_CHECKS):
                return
            if not all(type(value) is str and value in ('MATCH', 'INVALID_SHAPE') for value in shape.values()):
                return
            if not all(type(value) is str and value in ('MATCH', 'MISMATCH', 'INVALID_SHAPE') for value in checks.values()):
                return
            self.preflight_diagnostics = {'format': 'worktool-registration-preflight-fields-v1',
                'authority': 'DIAGNOSTIC_ONLY', 'classification': diagnostic['classification'],
                'shape': {key: shape[key] for key in ('root', 'row_count', 'row', 'fields')},
                'checks': {key: checks[key] for key in PREFLIGHT_CHECKS}}
        except Exception:
            return

    def execute(self, step):
        require(isinstance(step.phase, str) and bool(re.fullmatch('[a-z0-9_]{1,48}', step.phase)), 'INVALID_ARGUMENTS')
        self.stage = 'provision_' + step.phase
        self.record_execution_diagnostic(step.phase, 'SQL_PROCESS_PENDING')
        try:
            result = self.sql_call(step.login_role, step.database, step.sql, timeout=step.timeout_seconds)
        except BaseException as error:
            classification = {'SUBPROCESS_NONZERO': 'SQL_PROCESS_NONZERO', 'SUBPROCESS_TIMEOUT': 'SQL_PROCESS_TIMEOUT',
                'OUTPUT_LIMIT': 'SQL_PROCESS_OUTPUT_LIMIT', 'INTERRUPTED': 'INTERRUPTED'}.get(safe_code(error), 'SQL_PROCESS_FAILED')
            self.record_execution_diagnostic(step.phase, classification)
            raise
        self.record_execution_diagnostic(step.phase, 'SQL_PROCESS_ACKNOWLEDGED')
        if step.query:
            try:
                rows = parse_json(result)
            except BaseException as error:
                self.record_execution_diagnostic(step.phase, 'INTERRUPTED' if safe_code(error) == 'INTERRUPTED' else 'JSON_DECODE_FAILED')
                raise
            try:
                require(isinstance(rows, list) and len(rows) <= 4096 and all(isinstance(row, dict) for row in rows), 'INVALID_OUTPUT')
            except BaseException as error:
                self.record_execution_diagnostic(step.phase, 'INTERRUPTED' if safe_code(error) == 'INTERRUPTED' else 'JSON_SHAPE_INVALID')
                raise
            self.record_execution_diagnostic(step.phase, 'QUERY_RESULT_RETURNED')
            return rows
        try:
            require(result in (b'',), 'INVALID_OUTPUT')
        except BaseException as error:
            self.record_execution_diagnostic(step.phase, 'INTERRUPTED' if safe_code(error) == 'INTERRUPTED' else 'UNEXPECTED_NONQUERY_OUTPUT')
            raise
        self.record_execution_diagnostic(step.phase, 'EMPTY_RESULT_ACKNOWLEDGED')
        return []

    def retain_install_receipt(self):
        public = self.receipt.public_evidence
        expected_keys = {'format', 'status', 'sql_manifest_sha256', 'catalog_observation_sha256',
            'install_receipt_sha256', 'table_count', 'function_count', 'trigger_count', 'constraint_count',
            'constraint_semantic_normalization', 'acceptance_tests', 'execution', 'sql_sources'}
        require(isinstance(public, dict) and set(public) == expected_keys and
                public['format'] == 'worktool-registration-install-summary-v1' and
                public['status'] == 'VERIFIED_TRUSTED_INSTALLATION_PROVENANCE' and
                public['constraint_semantic_normalization'] == 'NOT_PROVEN' and
                public['acceptance_tests'] == 'NOT_RUN', 'PROVISION_FAILED')
        for key in ('sql_manifest_sha256', 'catalog_observation_sha256', 'install_receipt_sha256'):
            require(isinstance(public[key], str) and bool(HEX.fullmatch(public[key])), 'PROVISION_FAILED')
        for key in ('table_count', 'function_count', 'trigger_count', 'constraint_count'):
            require(type(public[key]) is int and 0 < public[key] <= 4096, 'PROVISION_FAILED')
        require(isinstance(public['execution'], list) and 1 <= len(public['execution']) <= 32 and
                public['sql_sources'] == dict(self.provision_helper.SOURCE_PINS), 'PROVISION_FAILED')
        for step in public['execution']:
            require(isinstance(step, dict) and set(step) == {'phase', 'login_role', 'database', 'sql_sha256', 'acknowledged'} and
                    isinstance(step['phase'], str) and bool(re.fullmatch('[a-z0-9_]{1,48}', step['phase'])) and
                    step['login_role'] in (self.admin, ROLE_NAMES[0]) and step['database'] in ('postgres', self.database) and
                    isinstance(step['sql_sha256'], str) and bool(HEX.fullmatch(step['sql_sha256'])) and step['acknowledged'] is True,
                    'PROVISION_FAILED')
        raw = self.provision_helper.canonical(self.receipt.private_catalog_receipt)
        require(len(raw) <= 4 << 20 and sha(raw) == public['install_receipt_sha256'], 'PROVISION_FAILED')
        # Retained locally for review only. The workflow upload whitelist contains
        # outcome.json, never this file or the private envelope/credentials.
        private_file(self.folder / 'private-install-receipt.json', raw)
        require(len(canonical(public)) <= 65536 and not any(secret.encode() in canonical(public) for secret in self.credentials.values()),
                'PROVISION_FAILED')
        self.install_summary = public
        self.stage = 'catalog_evidence'
        try:
            proof = self.provision_helper.sanitized_catalog_proof(self.receipt)
        except Exception:
            raise Fault('PROVISION_FAILED') from None
        require(isinstance(proof, dict) and set(proof) == {'format', 'proof_model', 'constraint_semantic_normalization',
            'sql_sources', 'sql_manifest_sha256', 'install_receipt_sha256', 'catalog_observation_sha256', 'execution', 'objects'} and
            proof['format'] == 'worktool-registration-catalog-proof-v1' and
            proof['constraint_semantic_normalization'] == 'NOT_PROVEN' and
            all(proof[key] == public[key] for key in ('sql_sources', 'sql_manifest_sha256', 'install_receipt_sha256', 'catalog_observation_sha256', 'execution')) and
            isinstance(proof['objects'], list) and 1 <= len(proof['objects']) <= 4096, 'PROVISION_FAILED')
        proof_bytes = self.provision_helper.canonical(proof)
        require(len(proof_bytes) <= 1 << 20 and not any(secret.encode() in proof_bytes for secret in self.credentials.values()), 'PROVISION_FAILED')
        private_file(self.folder / 'catalog-proof.json', proof_bytes)
        self.catalog_proof_sha256 = sha(proof_bytes)

    def provision(self):
        self.stage = 'ready'
        for _ in range(45):
            probe = self.call([DOCKER, 'exec', self.marker['container_id'], 'pg_isready', '-q',
                               '-h', '127.0.0.1', '-p', '5432', '-U', self.admin, '-d', 'postgres'],
                              check=False, timeout=5)
            if probe.returncode == 0:
                break
            require(probe.returncode in (1, 2), 'PG_READY_TIMEOUT')
            self.sleep(1)
        else:
            raise Fault('PG_READY_TIMEOUT')
        settings = self.sql_call(self.admin, 'postgres', "SELECT json_build_object('version',current_setting('server_version_num'),'version_string',version(),'connections',current_setting('max_connections'),'reserved',current_setting('reserved_connections'),'super_reserved',current_setting('superuser_reserved_connections'),'messages',current_setting('lc_messages'),'statement',current_setting('statement_timeout'),'lock',current_setting('lock_timeout'),'idle',current_setting('idle_in_transaction_session_timeout'),'data',current_setting('data_directory'))::text;")
        v = parse_json(settings)
        require(v.get('version') == '180006' and v.get('version_string', '').startswith('PostgreSQL 18.6 ') and
                v.get('connections') == '10' and v.get('reserved') == '0' and v.get('super_reserved') == '3' and v.get('messages') == 'C' and
                v.get('statement') == '5s' and v.get('lock') == '1s' and v.get('idle') == '5s' and
                v.get('data') == '/var/lib/postgresql/18/docker', 'PG_SETTINGS_MISMATCH')
        wal = self.call([DOCKER, 'exec', self.marker['container_id'], 'readlink', '-f',
                         '/var/lib/postgresql/18/docker/pg_wal']).stdout.strip()
        require(wal == b'/var/lib/postgresql/18/docker/pg_wal', 'PG_SETTINGS_MISMATCH')
        self.server_settings_verified = True
        self.verify_authentication()
        try:
            self.receipt = self.provision_helper.provision(self, self.bundle, self.database, self.admin,
                                                           {role: self.credentials[role] for role in ROLE_NAMES})
        except Fault:
            raise
        except Exception:
            raise Fault('PROVISION_FAILED') from None
        self.retain_install_receipt()
        # Every setup subprocess exited, therefore all setup/psql sessions have
        # closed. Independently inspect real activity before opening fixture.
        self.stage = 'bootstrap_close'
        active = self.sql_call(ROLE_NAMES[0], self.database,
            "SELECT count(*)::text FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND (backend_type='client backend' OR datid IS NOT NULL OR usename IS NOT NULL OR backend_type IS NULL);")
        require(active == b'0', 'PG_SETTINGS_MISMATCH')
        self.bootstrap_closed = True
        del self.credentials[self.admin]
        (self.folder / 'container.env').unlink()

    def build_environment(self):
        for name in ('cache', 'home', 'tmp'):
            (self.folder / name).mkdir(mode=0o700)
        return {'GOROOT': str(self.toolchain_root), 'HOME': str(self.folder / 'home'),
                'TMPDIR': str(self.folder / 'tmp'), 'GOCACHE': str(self.folder / 'cache'),
                'GOMODCACHE': str(self.config.module_cache), 'GOPATH': str(self.folder / 'home/go'),
                'GOPROXY': 'off', 'GOSUMDB': 'off', 'GOWORK': 'off', 'GOENV': 'off',
                'GOTOOLCHAIN': 'local', 'CGO_ENABLED': '0', 'GOFLAGS': '',
                'GOMAXPROCS': '2', 'GOTELEMETRY': 'off'}

    def compile_fixture(self):
        self.stage = 'build'
        self.environment = self.build_environment()
        self.module_verify_command = [str(self.config.go_binary), 'mod', 'verify']
        verified = self.call(self.module_verify_command, env=self.environment, cwd=str(self.config.source_root / 'backend-go'), timeout=60)
        require(verified.stdout.strip() == b'all modules verified', 'BUILD_FAILED')
        self.artifact = self.folder / 'fixture.test'
        self.build_command = [str(self.config.go_binary), 'test', '-c', '-vet=off', '-mod=readonly', '-p', '1',
                              '-o', str(self.artifact), './internal/store/pg']
        build = self.call(self.build_command, env=self.environment, cwd=str(self.config.source_root / 'backend-go'),
                          timeout=300, check=False)
        self.compile_exit_code = build.returncode
        require(build.returncode == 0, 'BUILD_FAILED')
        self.executable_sha256 = sha(read_regular(self.artifact, 128 << 20))
        self.verify_sources()

    def verify_sources(self):
        require(verify_pin(self.config.source_root, self.config.source_pin, self.config.source_pin_sha256) == self.source_sha256,
                'SOURCE_PIN_MISMATCH')
        verify_pin(self.toolchain_root, self.config.toolchain_pin, self.config.toolchain_pin_sha256, toolchain=True)

    def write_envelope(self):
        metadata = self.receipt.private_metadata
        provisioning = dict(format='worktool-registration-provisioning-evidence-v1', run_id=self.run_id,
            database=self.database, image=IMAGE, host='127.0.0.1', port=self.port, cpus=2,
            memory_bytes=1 << 30, memory_swap_bytes=1 << 30, shared_memory_bytes=64 << 20,
            tmpfs_bytes=2 << 30, pgdata='/var/lib/postgresql/18/docker', wal='/var/lib/postgresql/18/docker/pg_wal',
            fresh_database=True, bootstrap_closed=True, no_foreign_or_persistent_mounts=True,
            no_host_network_or_privileged=True, owned_resource_cleanup_plan='external-supervisor-cleans-exact-run-owned-resources-once')
        body = canonical(provisioning)
        private_file(self.folder / 'provisioning-evidence.json', body)
        self.provisioning_sha256 = sha(body)
        # Struct order is part of the fixture's strict canonical JSON ABI.
        envelope = dict(format='worktool-registration-isolated-fixture-v1', allow=ENV_ALLOW,
            run_id=self.run_id, host='127.0.0.1', port=self.port, database=self.database,
            database_oid=metadata['database_oid'], schema_oid=metadata['schema_oid'],
            consumer_oid=metadata['consumer_oid'], issuer_oid=metadata['issuer_oid'],
            environment=self.run_id, recovery_generation=self.run_id, image=IMAGE, server_version=180006,
            boundary_sha256=metadata['boundary_sha256'], registry_sha256=metadata['registry_sha256'],
            routine_definition_sha256=metadata['routine_definition_sha256'], source_manifest_sha256=self.source_sha256,
            executable_sha256=self.executable_sha256, provisioning_evidence_sha256=self.provisioning_sha256)
        for key, role in zip(('owner_dsn', 'issuer_dsn', 'consumer_dsn', 'unrelated_dsn'), ROLE_NAMES):
            envelope[key] = 'postgresql://' + role + ':' + self.credentials[role] + '@127.0.0.1:' + str(self.port) + '/' + self.database + '?sslmode=disable'
        private_file(self.folder / 'envelope.json', canonical(envelope))
        self.credentials.clear()

    def run_fixture(self):
        self.stage = 'fixture'
        self.verify_sources()
        require(sha(read_regular(self.artifact, 128 << 20)) == self.executable_sha256, 'EXECUTABLE_MISMATCH')
        view = self.inspect()
        self.resource_diagnostics['pre_fixture'] = container_diagnostics(
            view, self.identity, self.marker['owner'], self.marker['container_id'], self.image_id, self.port)
        require(verify_container(view, self.identity, self.marker['owner'], container_id=self.marker['container_id']) == self.port and
                view['image_id'] == self.image_id, 'RESOURCE_MISMATCH')
        require(self.marker['run_invocations'] == 0, 'RUN_ALREADY_STARTED')
        self.marker['run_invocations'] = 1
        self.marker['state'] = 'FIXTURE_STARTED'
        self.save_marker()
        command = [str(self.config.go_binary), 'tool', 'test2json', '-p', PACKAGE, str(self.artifact),
                   '-test.v=test2json', '-test.count=1', '-test.timeout=90s', '-test.run=^' + TEST + '$']
        environment = dict(self.environment, WORKTOOL_REGISTRATION_PG_ENVELOPE=str(self.folder / 'envelope.json'))
        try:
            result = self.call(command, env=environment,
                               cwd=str(self.config.source_root / 'backend-go/internal/store/pg'), timeout=110, check=False)
        except Fault as error:
            self.acceptance_events = error.partial_stdout
            raise
        self.acceptance_events = result.stdout
        self.stage = 'postflight'
        self.verify_sources()
        after = sha(read_regular(self.artifact, 128 << 20))
        require(after == self.executable_sha256, 'EXECUTABLE_MISMATCH')
        metadata = self.receipt.private_metadata
        self.binding = dict(format='worktool-registration-run-binding-v1', run_id=self.run_id,
            source_manifest_sha256=self.source_sha256, source_before_sha256=self.source_sha256,
            source_after_sha256=self.source_sha256, executable_before_sha256=self.executable_sha256,
            executable_after_sha256=after, command_sha256=sha(canonical(command)), exit_code=result.returncode,
            timed_out=False, interrupted=False, image=IMAGE, server_version=180006,
            database=self.database, database_oid=metadata['database_oid'], host='127.0.0.1', port=self.port,
            cleanup='NOT_CONFIRMED', bootstrap_closed=True, go_invocations=1, events_sha256=sha(result.stdout),
            compile_exit_code=self.compile_exit_code, build_command_sha256=sha(canonical(self.build_command)),
            schema_oid=metadata['schema_oid'], issuer_oid=metadata['issuer_oid'], consumer_oid=metadata['consumer_oid'],
            routine_definition_sha256=metadata['routine_definition_sha256'], provisioning_evidence_sha256=self.provisioning_sha256)
        require(result.returncode == 0, 'RUN_FAILED')
        self.evidence_helper.validate_events(result.stdout, self.cases)

    def authenticated_marker(self):
        checked_directory(self.folder, private=True)
        marker = parse_json(read_regular(self.folder / 'run-marker.json', 8192, private=True))
        require(isinstance(marker, dict) and set(marker) == {'format', 'identity', 'owner', 'container_name',
            'container_id', 'create_state', 'run_invocations', 'source_sha256', 'state', 'job_deadline_ns'} and
            marker['format'] == 'worktool-registration-owned-run-v1' and marker['identity'] == self.identity.data() and
            marker['container_name'] == self.identity.name and HEX.fullmatch(marker['owner']) and
            (marker['container_id'] == '' or HEX.fullmatch(marker['container_id'])) and
            marker['create_state'] in ('NOT_SENT', 'SENT', 'ACKNOWLEDGED', 'OBSERVED') and type(marker['run_invocations']) is int and marker['run_invocations'] in (0, 1) and
            type(marker['job_deadline_ns']) is int and marker['job_deadline_ns'] == self.config.job_deadline_ns and
            HEX.fullmatch(marker['source_sha256']) and marker['state'] in ('STARTED', 'FIXTURE_STARTED', 'FINISHED'),
            'CLEANUP_IDENTITY_MISMATCH')
        return marker

    def verify_standalone_cleanup_source(self):
        # A newly started cleanup process must use the originally pinned code.
        # Do not impose this on the already-loaded in-process finally path.
        marker = self.authenticated_marker()
        helper = self.evidence_helper or load_helper('pg-registration-evidence.py')
        root = Path(__file__).resolve().parents[1]
        try:
            cases = helper.load_manifest(root / 'scripts/pg-registration-cases.json')
            digest = helper.source_digest(root, cases)
        except Exception:
            raise Fault('SOURCE_PIN_MISMATCH') from None
        require(digest == marker['source_sha256'], 'SOURCE_PIN_MISMATCH')

    def cleanup(self):
        self.stage = 'cleanup'
        self.marker = self.authenticated_marker()
        self.operation_deadline = min(self.monotonic() + 60, self.marker['job_deadline_ns'] / 1_000_000_000 + 60)
        require(self.operation_deadline > self.monotonic(), 'CLEANUP_UNCONFIRMED')
        ids = self.existing_ids()
        if ids:
            require(self.marker['create_state'] != 'NOT_SENT', 'CLEANUP_IDENTITY_MISMATCH')
            view = self.inspect()
            try:
                verify_container(view, self.identity, self.marker['owner'], resources=False,
                                 container_id=self.marker['container_id'] or None)
                require(ids == [view['id']], 'CLEANUP_IDENTITY_MISMATCH')
            except Fault:
                raise Fault('CLEANUP_IDENTITY_MISMATCH') from None
            self.marker['container_id'] = view['id']
            self.marker['create_state'] = 'OBSERVED'
            self.save_marker()
            # One remove only. Any error/timeout is followed by observation, not
            # another removal; a later explicit cleanup starts with inspection.
            error = None
            try:
                self.call([DOCKER, 'rm', '-f', view['id']], timeout=30)
            except Fault as exc:
                error = exc
            remaining = self.existing_ids()
            require(not remaining, 'CLEANUP_UNCONFIRMED')
            if error is not None and not error.uncertain:
                raise Fault('CLEANUP_UNCONFIRMED')
        else:
            require(not self.existing_ids(), 'CLEANUP_UNCONFIRMED')
            # A daemon may finish an uncertain CREATE after both absence
            # observations. Absence is not a server-side cancellation receipt.
            require(self.marker['create_state'] != 'SENT', 'CLEANUP_UNCONFIRMED')
        # Keep the nonsecret exact-identity tombstone and redacted result, which
        # block run reentry for this hosted run/attempt. Never rmtree the ledger.
        allowed_files = {'container.env', 'envelope.json', 'provisioning-evidence.json', 'fixture.test'}
        allowed_dirs = {'cache', 'home', 'tmp'}
        try:
            for name in allowed_files:
                path = self.folder / name
                if path.exists() or path.is_symlink():
                    require(path.is_file() and not path.is_symlink(), 'CLEANUP_FILES_FAILED')
                    path.unlink()
            for name in allowed_dirs:
                path = self.folder / name
                if path.exists() or path.is_symlink():
                    require(path.is_dir() and not path.is_symlink(), 'CLEANUP_FILES_FAILED')
                    shutil.rmtree(path)
        except (OSError, Fault):
            raise Fault('CLEANUP_FILES_FAILED') from None
        self.credentials.clear()
        return 'CONFIRMED'

    def run(self, *, handle_signals=True):
        if self.started:
            return {'format': 'worktool-registration-outcome-v1', 'status': 'FAIL', 'run': 'NOT_RUN',
                    'failure_code': 'RUN_ALREADY_STARTED', 'failure_stage': 'preflight', 'cleanup': 'NOT_STARTED'}
        self.operation_deadline = min(self.monotonic() + 600, self.config.job_deadline_ns / 1_000_000_000 - 60)
        outcome = {'format': 'worktool-registration-outcome-v1', 'status': 'FAIL', 'run': 'NOT_RUN',
                   'failure_code': 'NOT_RUN', 'failure_stage': 'preflight', 'cleanup': 'NOT_STARTED'}
        try:
            require(type(self.config.job_deadline_ns) is int and self.monotonic() < self.config.job_deadline_ns / 1_000_000_000 <= self.monotonic() + 840, 'INVALID_ARGUMENTS')
            require(self.operation_deadline > self.monotonic(), 'SUBPROCESS_TIMEOUT')
            self.preflight()
            if handle_signals:
                self.install_signals()
            self.compile_fixture()
            self.create_container()
            self.provision()
            self.write_envelope()
            self.run_fixture()
            outcome.update(run='CANDIDATE_PASS', failure_code='OK', failure_stage='none')
        except BaseException as error:
            outcome.update(run='FAIL' if self.started else 'NOT_RUN', failure_code=safe_code(error), failure_stage=self.stage)
        finally:
            if self.started:
                # Cleanup stays bounded despite repeated ordinary cancellation.
                for sig in self.old_signals:
                    signal.signal(sig, signal.SIG_IGN)
                try:
                    outcome['cleanup'] = self.cleanup()
                except BaseException as error:
                    outcome['cleanup'] = 'UNCONFIRMED'
                    outcome['cleanup_code'] = safe_code(error, 'CLEANUP_UNCONFIRMED')
                outcome['source_sha256'] = self.source_sha256
                outcome['toolchain_sha256'] = self.toolchain_sha256
                if hasattr(self, 'executable_sha256'):
                    outcome['executable_sha256'] = self.executable_sha256
                    outcome['build_command_sha256'] = sha(canonical(self.build_command))
                    outcome['module_cache_verification'] = 'PASS_GO_MOD_VERIFY_OFFLINE'
                if hasattr(self, 'authentication_summary'):
                    outcome['authentication'] = self.authentication_summary
                if hasattr(self, 'install_summary'):
                    outcome['installation'] = self.install_summary
                if hasattr(self, 'catalog_proof_sha256'):
                    outcome['catalog_proof_sha256'] = self.catalog_proof_sha256
                if self.resource_diagnostics:
                    outcome['resource_diagnostics'] = self.resource_diagnostics
                if self.provision_diagnostics:
                    outcome['provision_diagnostics'] = {'format': 'worktool-registration-provision-phases-v1',
                        'authority': 'DIAGNOSTIC_ONLY', 'phases': dict(self.provision_diagnostics)}
                if self.preflight_diagnostics is not None:
                    outcome['preflight_diagnostics'] = self.preflight_diagnostics
                if hasattr(self, 'image_id'):
                    outcome['resource_policy'] = {'image': IMAGE, 'image_id': self.image_id, 'cpus': 2,
                        'memory_bytes': 1 << 30, 'memory_swap_bytes': 1 << 30, 'shm_bytes': 64 << 20,
                        'tmpfs_bytes': 2 << 30, 'max_connections': 10, 'host': '127.0.0.1',
                        'container_inspection': 'VERIFIED' if self.container_verified else 'NOT_VERIFIED',
                        'server_settings': 'VERIFIED' if self.server_settings_verified else 'NOT_VERIFIED'}
                    if self.container_verified:
                        outcome['container_observation'] = self.container_observation
                if self.interrupted:
                    outcome.update(run='FAIL', failure_code='INTERRUPTED')
                if outcome['run'] == 'CANDIDATE_PASS' and outcome['cleanup'] == 'CONFIRMED':
                    try:
                        self.binding['cleanup'] = 'CONFIRMED'
                        report = self.evidence_helper.validate_run(self.acceptance_events, self.cases, self.binding, self.source_sha256)
                        outcome.update(status='PASS_FOCUSED_ISOLATED_SCOPE', run='PASS', evidence=report)
                    except BaseException:
                        outcome.update(run='FAIL', failure_code='EVIDENCE_REJECTED')
                if outcome['status'] != 'PASS_FOCUSED_ISOLATED_SCOPE' and hasattr(self, 'cases') and hasattr(self.evidence_helper, 'failure_summary'):
                    try:
                        outcome['diagnostics'] = self.evidence_helper.failure_summary(self.acceptance_events, self.cases)
                    except Exception:
                        outcome['diagnostics'] = {'acceptance': 'NOT_ACCEPTED', 'diagnostic': 'DIAGNOSTICS_UNAVAILABLE'}
                self.marker['state'] = 'FINISHED'
                try:
                    self.save_marker()
                    replace_private(self.folder / 'outcome.json', outcome)
                except BaseException:
                    outcome.update(status='FAIL', failure_code='CLEANUP_FILES_FAILED')
                self.restore_signals()
        return outcome


def parser():
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest='action')
    sub.add_parser('noop')
    sub.add_parser('selftest')
    for action in ('make-source-pin', 'make-toolchain-pin', 'stage-toolchain'):
        q = sub.add_parser(action)
        q.add_argument('--root', required=True)
        q.add_argument('--output', required=True)
    for action in ('run', 'cleanup'):
        q = sub.add_parser(action)
        for option in ('repository', 'commit', 'ref', 'run-id', 'run-attempt'):
            q.add_argument('--' + option, required=True)
        if action == 'run':
            for option in ('source-root', 'source-manifest', 'manifest-sha256', 'go-binary',
                           'toolchain-manifest', 'toolchain-manifest-sha256', 'module-cache'):
                q.add_argument('--' + option, required=True)
    return p


def main(argv=None, *, executor=None, environ=None):
    env = dict(os.environ if environ is None else environ)
    try:
        args = parser().parse_args(argv)
        if args.action in (None, 'noop'):
            print('{"status":"NOT_RUN","reason":"explicit hosted run or cleanup command required"}')
            return 0
        if args.action == 'selftest':
            import unittest
            suite = unittest.defaultTestLoader.discover(str(Path(__file__).parent), 'test_pg_registration_acceptance.py')
            return 0 if unittest.TextTestRunner(verbosity=2).run(suite).wasSuccessful() else 1
        if args.action == 'stage-toolchain':
            result = stage_toolchain(Path(args.root), Path(args.output))
            print(json.dumps(result, separators=(',', ':')))
            return 0
        if args.action in ('make-source-pin', 'make-toolchain-pin'):
            body = make_pin(Path(args.root), toolchain=args.action == 'make-toolchain-pin')
            output = Path(args.output)
            require(output.is_absolute() and output.parent.resolve() == output.parent and
                    not output.is_relative_to(Path(args.root).resolve()), 'UNSAFE_PATH')
            private_file(output, body)
            print(json.dumps({'status': 'PIN_CREATED_NOT_EXECUTED', 'sha256': sha(body)}))
            return 0
        identity = Identity.checked(args.repository, args.commit, args.ref, args.run_id, args.run_attempt, env)
        temp = checked_directory(Path(env.get('RUNNER_TEMP', '')))
        raw_deadline = env.get('WORKTOOL_REGISTRATION_JOB_DEADLINE_NS', '')
        require(bool(re.fullmatch('[1-9][0-9]{0,19}', raw_deadline)), 'INVALID_ARGUMENTS')
        job_deadline = int(raw_deadline)
        require(job_deadline / 1_000_000_000 <= time.monotonic() + 840, 'INVALID_ARGUMENTS')
        if args.action == 'cleanup':
            config = RunConfig(identity, temp, Path('/unused'), Path('/unused'), '', Path('/unused'), Path('/unused'), '', Path('/unused'), job_deadline)
            supervisor = Supervisor(config, executor=executor)
            if not supervisor.folder.exists() and not supervisor.folder.is_symlink():
                # No marker means no authority to remove any matching container.
                # Absence is reported as NO_OWNED_RUN, not cleanup success.
                print('{"status":"NO_OWNED_RUN","cleanup":"NOT_STARTED"}')
                return 0
            supervisor.verify_standalone_cleanup_source()
            cleanup = supervisor.cleanup()
            private = supervisor.folder / 'outcome.json'
            result = {'status': 'CLEANUP_ONLY', 'cleanup': cleanup}
            if private.exists():
                previous = parse_json(read_regular(private, 1 << 20, private=True))
                previous['subsequent_cleanup'] = cleanup
                replace_private(private, previous)
            print(json.dumps(result))
            return 0
        config = RunConfig(identity, temp, Path(args.source_root), Path(args.source_manifest), args.manifest_sha256,
                           Path(args.go_binary), Path(args.toolchain_manifest), args.toolchain_manifest_sha256,
                           Path(args.module_cache), job_deadline)
        result = Supervisor(config, executor=executor).run()
        print(json.dumps(result, separators=(',', ':')))
        return 0 if result['status'] == 'PASS_FOCUSED_ISOLATED_SCOPE' else 1
    except (Exception, KeyboardInterrupt) as error:
        failure = {'status': 'FAIL', 'code': safe_code(error)}
        if 'args' in locals() and args.action == 'cleanup':
            failure['cleanup'] = 'UNCONFIRMED'
        print(json.dumps(failure))
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
