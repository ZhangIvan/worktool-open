#!/usr/bin/env python3
"""Pinned official Go acquisition. Never execute a member; fail closed before publishing its path."""
import hashlib
import http.client
import json
import os
from pathlib import PurePosixPath
import signal
import re
import ssl
import socket
import urllib.error
import stat
import sys
import secrets
import time
import urllib.parse
import urllib.request
import zlib

URL = 'https://go.dev/dl/go1.27.3.linux-amd64.tar.gz'
COMPRESSED_SIZE = 70588678
COMPRESSED_PIN = '65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40'
TREE_PIN = 'da3e906bf3b01e50e398146823c6bf2b16a22951597834c5f4808ec38ca5e308'
GO_PIN = '5f5eb6d0b2fb6aa0bb5d5f8c5f1c944e0de955c925f0111af92a9d8f79680b5b'
MAX_COMPRESSED = COMPRESSED_SIZE
MAX_RAW = 300 * 1024 * 1024
MAX_FILE = 32 * 1024 * 1024
MAX_OUTPUT = 8 * 1024 * 1024
MAX_ENTRIES = 18000
BLOCK = 65536
DEADLINE = None

class Stop(Exception):
    pass

PHASES = ('storage', 'tls_setup', 'open', 'transfer', 'hashverify', 'parse', 'cleanup', 'output')
SAFE_REASONS = frozenset(['ARCHIVE_PATH', 'ARCHIVE_TYPE', 'ARGUMENTS', 'CHANGED', 'CLEANUP', 'CLEANUP_IDENTITY', 'CLEANUP_UNCONFIRMED', 'COMPRESSED_HASH', 'COMPRESSED_LIMIT', 'COMPRESSED_SIZE', 'DUPLICATE', 'ENTRY_LIMIT', 'EXTENSION_DANGLING', 'EXTENSION_LIMIT', 'FILE_LIMIT', 'GET', 'GNU_EXTENSION', 'GNU_LONGNAME', 'GZIP_TRAILING', 'GZIP_TRUNCATED', 'HTTP_ENCODING', 'HTTP_SIZE', 'HTTP_STATUS', 'INTERNAL_READ_LIMIT', 'IO_OR_FORMAT', 'MATCH', 'MISMATCH', 'NOT_RUN', 'OUTPUT_LIMIT', 'PATH', 'PATH_CONFLICT', 'PAX_KEY', 'PAX_LENGTH', 'PRIVATE_BASE', 'RAW_LIMIT', 'REDIRECT_LIMIT', 'REDIRECT_LOCATION', 'REDIRECT_STATUS', 'REDIRECT_TARGET', 'SCRATCH_OWNER', 'TAR_ALIGNMENT', 'TAR_CHECKSUM', 'TAR_DEVICE', 'TAR_END', 'TAR_FORMAT', 'TAR_LINK', 'TAR_NUMBER', 'TAR_PADDING', 'TAR_RESERVED', 'TAR_STRING_PADDING', 'TAR_TRAILING', 'TAR_TRUNCATED', 'TIME_LIMIT', 'TMP_OWNER', 'TMP_ROOT', 'TRUNCATED', 'UNCONFIRMED', 'UNSUPPORTED_TAR_TYPE', 'WRITE'])

class Trace:
    def __init__(self):
        self.phase = 'storage'
        self.completed = []
        self.cleanup = 'not_needed'
        self.primary = None
        self.cleanup_errors = []
        self.secondary_errors = []
    def enter(self, phase):
        assert phase in PHASES
        self.phase = phase
    def finish(self):
        if self.phase not in self.completed:
            self.completed.append(self.phase)
    def failed(self, exc):
        return safe_error(exc, self.phase)
    def report(self):
        return {'schema': 2, 'diagnostic_only': True, 'pg_acceptance': 'NOT_RUN',
                'status': 'UNCONFIRMED', 'primary_error': self.primary,
                'cleanup_errors': self.cleanup_errors, 'secondary_errors': self.secondary_errors,
                'cleanup_status': self.cleanup,
                'completed_phases': self.completed,
                'incomplete_phases': [p for p in PHASES if p not in self.completed]}

class Failure(Stop):
    def __init__(self, trace):
        self.trace = trace
        super().__init__('UNCONFIRMED')

def safe_error(exc, phase):
    result = {'phase': phase, 'error_kind': 'UNKNOWN'}
    if isinstance(exc, urllib.error.HTTPError):
        result['error_kind'] = 'HTTP'
        if type(exc.code) is int and 100 <= exc.code <= 599:
            result['http_status'] = exc.code
        return result
    if isinstance(exc, urllib.error.URLError):
        # Only inspect the nested exception type; never stringify reason.
        inner = exc.reason
        if isinstance(inner, BaseException) and not isinstance(inner, urllib.error.URLError):
            result = safe_error(inner, phase)
        else:
            result['error_kind'] = 'URL_OPEN'
        return result
    if isinstance(exc, Stop):
        result['error_kind'] = 'POLICY_OR_FORMAT'
        reason = exc.args[0] if len(exc.args) == 1 and type(exc.args[0]) is str else None
        result['reason'] = reason if reason in SAFE_REASONS else 'UNKNOWN_STOP'
    elif isinstance(exc, ssl.SSLCertVerificationError):
        result['error_kind'] = 'TLS_CERTIFICATE_VERIFY'
    elif isinstance(exc, ssl.SSLError):
        result['error_kind'] = 'TLS'
    elif isinstance(exc, socket.gaierror):
        result['error_kind'] = 'DNS'
    elif isinstance(exc, TimeoutError):
        result['error_kind'] = 'TIMEOUT'
    elif isinstance(exc, PermissionError):
        result['error_kind'] = 'PERMISSION'
    elif isinstance(exc, FileNotFoundError):
        result['error_kind'] = 'NOT_FOUND'
    elif isinstance(exc, ConnectionError):
        result['error_kind'] = 'CONNECTION'
    elif isinstance(exc, OSError):
        result['error_kind'] = 'OS_IO'
    elif isinstance(exc, zlib.error):
        result['error_kind'] = 'GZIP'
    elif isinstance(exc, http.client.HTTPException):
        result['error_kind'] = 'HTTP_PROTOCOL'
    elif isinstance(exc, UnicodeError):
        result['error_kind'] = 'TEXT_ENCODING'
    elif isinstance(exc, ValueError):
        result['error_kind'] = 'VALUE_OR_FORMAT'
    elif isinstance(exc, RuntimeError):
        result['error_kind'] = 'RUNTIME'
    return result

def need(ok, reason):
    if not ok:
        raise Stop(reason)

def expired(*unused):
    raise Stop('TIME_LIMIT')

def arm(seconds):
    remaining = seconds if DEADLINE is None else min(seconds, DEADLINE - time.monotonic())
    signal.setitimer(signal.ITIMER_REAL, max(0.001, remaining))

class Budget:
    def __init__(self, seconds):
        self.end = time.monotonic() + seconds
    def check(self):
        need(time.monotonic() < self.end, 'TIME_LIMIT')

class Redirect(urllib.request.HTTPRedirectHandler):
    def __init__(self, budget):
        self.count = 0
        self.budget = budget
    def http_error_302(self, req, fp, code, msg, headers):
        # urllib's default implementation drains the redirect body unboundedly.
        # Close it without reading; only follow a validated absolute HTTPS target.
        try:
            self.budget.check()
            self.count += 1
            need(self.count <= 3, 'REDIRECT_LIMIT')
            need(sum(len(k) + len(v) + 4 for k,v in headers.items()) <= 32768, 'HTTP_SIZE')
            locations = headers.get_all('Location')
            need(locations is not None and len(locations) == 1, 'REDIRECT_LOCATION')
            newurl = locations[0]
            request = self.redirect_request(req, fp, code, msg, headers, newurl)
        finally:
            fp.close()
        return self.parent.open(request, timeout=min(10, max(0.01, self.budget.end - time.monotonic())))
    http_error_301 = http_error_303 = http_error_307 = http_error_308 = http_error_302
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        self.budget.check()
        u = urllib.parse.urlsplit(newurl)
        need(len(newurl) <= 8192 and u.scheme == 'https' and newurl == 'https://dl.google.com/go/go1.27.3.linux-amd64.tar.gz'
             and u.port in (None, 443) and not u.username and not u.password
             and not u.fragment and not any(ord(c) < 33 or ord(c) > 126 for c in newurl), 'REDIRECT_TARGET')
        need(code in (301, 302, 303, 307, 308), 'REDIRECT_STATUS')
        return urllib.request.Request(newurl, headers={'Accept-Encoding': 'identity'}, method='GET')

def download(fd, trace=None):
    trace = trace or Trace()
    trace.enter('tls_setup')
    budget = Budget(60)
    arm(60)
    # Explicit bounded HTTP parser: <=40 lines of <=8192 bytes per response.
    http.client._MAXLINE = 8192
    http.client._MAXHEADERS = 40
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    context.load_verify_locations(cafile='/etc/ssl/certs/ca-certificates.crt')
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), Redirect(budget),
                urllib.request.HTTPSHandler(context=context))
    # No auth, cookies, environment proxy, retries, alternate URL or credentials.
    request = urllib.request.Request(URL, headers={'Accept-Encoding': 'identity'}, method='GET')
    trace.finish()
    trace.enter('open')
    count = 0
    with opener.open(request, timeout=10) as response:
        if response.status != 200:
            raise urllib.error.HTTPError('', response.status, '', None, None)
        trace.finish()
        trace.enter('transfer')
        need(sum(len(k) + len(v) + 4 for k,v in response.headers.items()) <= 32768, 'HTTP_SIZE')
        # Reject chunk framing (including unbounded trailer metadata), duplicate
        # framing fields and encoded bodies before the first entity-body read.
        need(response.headers.get_all('Transfer-Encoding') is None, 'HTTP_ENCODING')
        encodings = response.headers.get_all('Content-Encoding')
        need(encodings is None or encodings == ['identity'], 'HTTP_ENCODING')
        lengths = response.headers.get_all('Content-Length')
        need(lengths is None or lengths == [str(COMPRESSED_SIZE)], 'HTTP_SIZE')
        while True:
            budget.check()
            data = response.read(min(BLOCK, MAX_COMPRESSED + 1 - count))
            if not data:
                break
            count += len(data)
            need(count <= MAX_COMPRESSED, 'COMPRESSED_LIMIT')
            view = memoryview(data)
            while view:
                n = os.write(fd, view)
                need(n > 0, 'WRITE')
                view = view[n:]
    budget.check()
    need(count == COMPRESSED_SIZE, 'COMPRESSED_SIZE')
    trace.finish()
    arm(190)

def ident(s):
    return (s.st_dev, s.st_ino, s.st_mode, s.st_nlink, s.st_size, s.st_mtime_ns, s.st_ctime_ns)

def verify(fd):
    before = os.fstat(fd)
    need(stat.S_ISREG(before.st_mode) and before.st_nlink == 1, 'ARCHIVE_TYPE')
    need(before.st_size == COMPRESSED_SIZE, 'COMPRESSED_SIZE')
    os.lseek(fd, 0, os.SEEK_SET)
    digest = hashlib.sha256()
    remaining = COMPRESSED_SIZE
    while remaining:
        data = os.read(fd, min(BLOCK, remaining))
        need(data, 'TRUNCATED')
        remaining -= len(data)
        digest.update(data)
    need(not os.read(fd, 1) and ident(os.fstat(fd)) == ident(before), 'CHANGED')
    need(digest.hexdigest() == COMPRESSED_PIN, 'COMPRESSED_HASH')
    os.lseek(fd, 0, os.SEEK_SET)
    return ident(before)

class Stream:
    """Single gzip member, bounded output per inflate, explicit CRC/EOF validation."""
    def __init__(self, fd):
        self.fd = fd
        self.z = zlib.decompressobj(31)
        self.pending = b''
        self.buf = bytearray()
        self.raw = 0
        self.compressed = 0
        self.done = False
        self.budget = Budget(120)
    def read(self, n):
        need(0 <= n <= BLOCK, 'INTERNAL_READ_LIMIT')
        while len(self.buf) < n and not self.done:
            self.budget.check()
            if not self.pending:
                self.pending = os.read(self.fd, BLOCK)
                self.compressed += len(self.pending)
                need(self.compressed <= MAX_COMPRESSED, 'COMPRESSED_LIMIT')
                need(self.pending, 'GZIP_TRUNCATED')
            out = self.z.decompress(self.pending, min(BLOCK, n - len(self.buf)))
            self.pending = self.z.unconsumed_tail
            self.raw += len(out)
            need(self.raw <= MAX_RAW, 'RAW_LIMIT')
            self.buf.extend(out)
            if self.z.eof:
                need(not self.z.unused_data and not self.pending and not os.read(self.fd, 1), 'GZIP_TRAILING')
                self.done = True
        out = bytes(self.buf[:n])
        del self.buf[:n]
        return out
    def exact(self, n):
        data = self.read(n)
        need(len(data) == n, 'TAR_TRUNCATED')
        return data

def textfield(raw):
    first, sep, tail = raw.partition(b'\0')
    need(not sep or not any(tail), 'TAR_STRING_PADDING')
    return first.decode('utf-8', 'strict')

def number(raw):
    # Strict octal only; base-256, sign, overflow and embedded terminators rejected.
    need(re.fullmatch(rb' *[0-7]+(?:\x00 *| +)?', raw) is not None, 'TAR_NUMBER')
    return int(raw.rstrip(b'\0 ').lstrip(b' '), 8)

def path(raw, directory):
    need(len(raw.encode('utf-8')) <= 514 and raw.isprintable(), 'PATH')
    p = raw
    if directory and p.endswith('/'):
        p = p[:-1]
    parts = p.split('/')
    need(p and len(p.encode('utf-8')) <= 512 and len(parts) <= 32
         and all(x and x not in ('.', '..') for x in parts), 'PATH')
    return p

def parse(fd, compressed_verified=False, sink=None):
    stream = Stream(fd)
    entries = {}
    parent_paths = set()
    root_seen = False
    members = 0
    pending_path = None
    extensions = 0
    longnames = 0
    while True:
        h = stream.exact(512)
        if h == bytes(512):
            need(pending_path is None, 'EXTENSION_DANGLING')
            need(stream.exact(512) == bytes(512), 'TAR_END')
            # Remaining tar blocking padding must be zero, and counts toward raw budget.
            while True:
                padding = stream.read(BLOCK)
                if not padding:
                    break
                need(not any(padding), 'TAR_TRAILING')
            need(stream.raw % 512 == 0, 'TAR_ALIGNMENT')
            break
        members += 1
        need(members <= MAX_ENTRIES, 'ENTRY_LIMIT')
        need(number(h[148:156]) == sum(h[:148]) + 8 * 32 + sum(h[156:]), 'TAR_CHECKSUM')
        need(h[257:265] in (b'ustar\x0000', b'ustar  \x00'), 'TAR_FORMAT')
        kind = h[156:157]
        # Only a bounded local PAX path override is supported.
        need(kind in (b'0', b'\0', b'5', b'x', b'L'), 'UNSUPPORTED_TAR_TYPE')
        need(not any(h[157:257]), 'TAR_LINK')
        for a, b in ((100,108), (108,116), (116,124), (136,148)):
            number(h[a:b])
        size = number(h[124:136])
        directory = kind == b'5'
        need(size <= MAX_FILE and (not directory or size == 0), 'FILE_LIMIT')
        name = textfield(h[:100])
        for field in (h[329:337], h[337:345]):
            need(not any(field) or number(field) == 0, 'TAR_DEVICE')
        if h[257:265] == b'ustar  \x00':
            need(not any(h[345:512]), 'GNU_EXTENSION')
            prefix = ''
        else:
            need(not any(h[500:512]), 'TAR_RESERVED')
            prefix = textfield(h[345:500])
        raw = prefix + '/' + name if prefix else name
        header_path = raw
        if kind == b'L':
            need(raw == '././@LongLink' and pending_path is None and 1 < size <= 515, 'GNU_LONGNAME')
            data = stream.exact(size)
            need(data[-1:] == b'\0' and b'\0' not in data[:-1], 'GNU_LONGNAME')
            need(not any(stream.exact((-size) % 512)), 'TAR_PADDING')
            pending_path = data[:-1].decode('utf-8', 'strict')
            path(pending_path, pending_path.endswith('/'))
            longnames += 1
            continue
        if not (kind == b'x' and raw == '././@PaxHeader'):
            path(raw, directory)  # Only exact known metadata placeholder is exempt.
        if kind == b'x':
            need(pending_path is None and 0 < size <= 4096, 'EXTENSION_LIMIT')
            data = stream.exact(size)
            need(not any(stream.exact((-size) % 512)), 'TAR_PADDING')
            # Exactly one self-sized path record; all other keys fail closed.
            digits, sep, rest = data.partition(b' ')
            need(sep and 1 <= len(digits) <= 4 and digits.isdigit()
                 and not digits.startswith(b'0') and int(digits) == len(data), 'PAX_LENGTH')
            need(rest.startswith(b'path=') and rest.endswith(b'\n')
                 and b'\n' not in rest[:-1] and b'\0' not in rest, 'PAX_KEY')
            pending_path = rest[5:-1].decode('utf-8', 'strict')
            path(pending_path, pending_path.endswith('/'))
            extensions += 1
            continue
        if pending_path is not None:
            raw = pending_path
            pending_path = None
        p = path(raw, directory)
        need(p == 'go' and directory or p.startswith('go/'), 'PATH')
        p = p[3:] if p.startswith('go/') else ''
        mode = number(h[100:108])
        need(mode in (0o644, 0o755) and (not directory or mode == 0o755), 'ARCHIVE_TYPE')
        if not p:
            need(not root_seen, 'DUPLICATE')
            root_seen = True
            continue
        need(p not in entries, 'DUPLICATE')
        parts = p.split('/')
        need(all(entries.get('/'.join(parts[:i]), {}).get('type') != 'file'
                 for i in range(1, len(parts))), 'PATH_CONFLICT')
        if not directory:
            need(p not in parent_paths, 'PATH_CONFLICT')
        parent_paths.update('/'.join(parts[:i]) for i in range(1, len(parts)))
        if sink is not None:
            sink.begin(p, directory, size, mode)
        digest = hashlib.sha256()
        left = size
        while left:
            data = stream.exact(min(BLOCK, left))
            digest.update(data)
            if sink is not None:
                sink.write(data)
            left -= len(data)
        padding = (-size) % 512
        need(not any(stream.exact(padding)), 'TAR_PADDING')
        if sink is not None:
            sink.end(p, directory, size, digest.hexdigest())
        entries[p] = {'mode': mode, 'path': p, 'raw_path': raw, 'header_path': header_path, 'type': 'directory' if directory else 'file',
                      'size': size, 'sha256': None if directory else digest.hexdigest()}
    ordered = [entries[k] for k in sorted(entries, key=lambda x: PurePosixPath(x).parts)]
    digest = hashlib.sha256()
    for row in ordered:
        if row['type'] == 'file':
            digest.update(row['path'].encode() + b'\0' + bytes.fromhex(row['sha256']))
    actual = digest.hexdigest()
    binary = entries.get('bin/go', {}).get('sha256')
    status = 'MATCH' if actual == TREE_PIN and binary == GO_PIN else 'MISMATCH'
    summary = {'schema': 1, 'diagnostic_only': True, 'pg_acceptance': 'NOT_RUN',
               'compressed_identity_verified': compressed_verified, 'status': status, 'tree_sha256': actual, 'go_sha256': binary,
               'expected_tree_sha256': TREE_PIN, 'expected_go_sha256': GO_PIN,
               'expected_compressed_sha256': COMPRESSED_PIN, 'expected_compressed_size': COMPRESSED_SIZE,
               'entries': len(entries), 'files': sum(r['type'] == 'file' for r in ordered),
               'tar_members': members, 'pax_path_records': extensions, 'gnu_longname_records': longnames, 'root_container_raw_path': 'go' if root_seen else None, 'uncompressed_bytes': stream.raw}
    need(root_seen, 'PATH')
    need(status == 'MATCH', 'MISMATCH')
    need(summary['files'] == 15657 and len(entries) == 17372, 'ENTRY_LIMIT')
    need(sum(r['size'] for r in ordered) == 244139206, 'FILE_LIMIT')
    return entries, summary

def open_existing(value):
    need(value.startswith('/') and len(value) <= 1024, 'ARCHIVE_PATH')
    parts = value.split('/')[1:]
    need(len(parts) <= 32 and all(p and p not in ('.', '..') for p in parts), 'ARCHIVE_PATH')
    d = os.open('/', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        for part in parts[:-1]:
            nxt = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=d)
            os.close(d)
            d = nxt
        return os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=d)
    finally:
        os.close(d)

def scratch_base(private):
    d = os.open('/', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        if private:
            for component in (None, 'workspace', 'shared'):
                if component is not None:
                    nxt = os.open(component, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=d)
                    os.close(d)
                    d = nxt
                st = os.fstat(d)
                need(st.st_uid in (0, os.getuid()) and not stat.S_IMODE(st.st_mode) & 0o022, 'PRIVATE_BASE')
        else:
            st = os.fstat(d)
            need(st.st_uid == 0 and not stat.S_IMODE(st.st_mode) & 0o022, 'TMP_ROOT')
            nxt = os.open('tmp', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=d)
            os.close(d)
            d = nxt
            st = os.fstat(d)
            need(st.st_uid == 0 and stat.S_IMODE(st.st_mode) == 0o1777, 'TMP_OWNER')
        return d
    except BaseException:
        os.close(d)
        raise


# This is not a sandbox against root or a hostile process with the same UID.
# All owned state is private. Partial/unknown state is deliberately retained.
MAX_BLOCKS = 512 * 1024 * 1024
MAX_RECEIPT = 8 * 1024 * 1024
MAX_CONTENT = 244139206
TREE_BLOCK_BUDGET = MAX_BLOCKS - COMPRESSED_SIZE - MAX_RECEIPT - 2 * 1024 * 1024
EXECUTABLES = frozenset(('bin/go', 'bin/gofmt', *('pkg/tool/linux_amd64/' + x for x in
    ('asm', 'cgo', 'compile', 'cover', 'fix', 'link', 'preprofile', 'vet'))))
FLAGS = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW

def owned_dir(fd):
    s = os.fstat(fd)
    need(stat.S_ISDIR(s.st_mode) and s.st_uid == os.getuid()
         and stat.S_IMODE(s.st_mode) == 0o700, 'SCRATCH_OWNER')
    return (s.st_dev, s.st_ino)

def parent_fd(root, relative):
    parts = relative.split('/')
    need(path(relative, False) == relative, 'PATH')
    fd = os.dup(root)
    try:
        owned_dir(fd)
        for part in parts[:-1]:
            nxt = os.open(part, FLAGS, dir_fd=fd)
            os.close(fd)
            fd = nxt
            owned_dir(fd)
        return fd, parts[-1]
    except BaseException:
        os.close(fd)
        raise

def record(s):
    return [s.st_dev, s.st_ino, s.st_size, stat.S_IMODE(s.st_mode), s.st_nlink]

def write_all(fd, data):
    view = memoryview(data)
    while view:
        n = os.write(fd, view)
        need(n > 0, 'WRITE')
        view = view[n:]

class Materializer:
    def __init__(self, root, expected):
        self.root = root
        self.expected = expected
        self.current = None
        self.seen = set()
        self.blocks = os.fstat(root).st_blocks * 512
        self.total = 0
        self.identities = {}
    def begin(self, p, directory, size, mode):
        row = self.expected.get(p)
        need(row is not None and p not in self.seen and row['size'] == size
             and row['mode'] == mode and (row['type'] == 'directory') == directory, 'CHANGED')
        self.seen.add(p)
        # Reserve conservative per-object overhead before creation. st_blocks is
        # checked after each <=64KiB write; this is detection, not a filesystem quota.
        need(self.blocks + ((size + 4095)//4096)*4096 + 65536 <= TREE_BLOCK_BUDGET, 'FILE_LIMIT')
        d, name = parent_fd(self.root, p)
        try:
            if directory:
                os.mkdir(name, 0o700, dir_fd=d)
                fd = os.open(name, FLAGS, dir_fd=d)
                try:
                    owned_dir(fd)
                    self.identities[p] = record(os.fstat(fd))
                    self.blocks += os.fstat(fd).st_blocks * 512
                finally:
                    os.close(fd)
            else:
                self.current = os.open(name, os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW, 0o600, dir_fd=d)
        finally:
            os.close(d)
    def write(self, data):
        need(self.current is not None and self.total + len(data) <= MAX_CONTENT, 'FILE_LIMIT')
        write_all(self.current, data)
        self.total += len(data)
        need(self.blocks + os.fstat(self.current).st_blocks * 512 <= TREE_BLOCK_BUDGET, 'FILE_LIMIT')
    def end(self, p, directory, size, digest):
        if directory:
            return
        need(digest == self.expected[p]['sha256'], 'CHANGED')
        fd, self.current = self.current, None
        try:
            s = os.fstat(fd)
            need(stat.S_ISREG(s.st_mode) and s.st_uid == os.getuid() and s.st_nlink == 1
                 and s.st_size == size and stat.S_IMODE(s.st_mode) == 0o600, 'CHANGED')
            self.blocks += s.st_blocks * 512
            self.identities[p] = record(s)
            os.fsync(fd)
        finally:
            os.close(fd)


def scan(root, expected=None, executable=False):
    """Bounded, nofollow FD scan. Verify every file, directory and original pin."""
    files, dirs, total, blocks = 0, 0, 0, os.fstat(root).st_blocks * 512
    rows = {}
    budget = Budget(40)
    def visit(fd, prefix):
        nonlocal files, dirs, total, blocks
        owned_dir(fd)
        names = os.listdir(fd)
        need(len(names) <= MAX_ENTRIES and len(rows) + len(names) <= MAX_ENTRIES, 'ENTRY_LIMIT')
        for name in sorted(names):
            budget.check()
            p = prefix + name
            need(path(p, False) == p, 'PATH')
            st = os.stat(name, dir_fd=fd, follow_symlinks=False)
            need(st.st_uid == os.getuid(), 'SCRATCH_OWNER')
            blocks += st.st_blocks * 512
            need(blocks <= TREE_BLOCK_BUDGET, 'FILE_LIMIT')
            if stat.S_ISDIR(st.st_mode):
                dirs += 1
                child = os.open(name, FLAGS, dir_fd=fd)
                try:
                    need(record(os.fstat(child)) == record(st), 'CHANGED')
                    rows[p] = {'type': 'directory', 'identity': record(st)}
                    visit(child, p + '/')
                finally:
                    os.close(child)
            else:
                need(stat.S_ISREG(st.st_mode) and st.st_nlink == 1 and st.st_size <= MAX_FILE
                     and stat.S_IMODE(st.st_mode) == (0o700 if executable and p in EXECUTABLES else 0o600), 'ARCHIVE_TYPE')
                files += 1
                total += st.st_size
                need(files <= 15657 and dirs <= 1715 and total <= MAX_CONTENT, 'FILE_LIMIT')
                f = os.open(name, os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK, dir_fd=fd)
                try:
                    before = os.fstat(f)
                    need(ident(before) == ident(st), 'CHANGED')
                    digest = hashlib.sha256()
                    left = st.st_size
                    while left:
                        budget.check()
                        data = os.read(f, min(BLOCK, left))
                        need(data, 'TRUNCATED')
                        digest.update(data)
                        left -= len(data)
                    need(not os.read(f,1) and ident(os.fstat(f)) == ident(before), 'CHANGED')
                    rows[p] = {'type': 'file', 'sha256': digest.hexdigest(), 'identity': record(st)}
                finally:
                    os.close(f)
            if expected is not None:
                row = expected.get(p)
                need(row is not None and row['type'] == rows[p]['type'], 'CHANGED')
                if rows[p]['type'] == 'file':
                    need(row['sha256'] == rows[p]['sha256'] and row['size'] == st.st_size, 'CHANGED')
    visit(root, '')
    need(files == 15657 and dirs == 1715 and total == MAX_CONTENT, 'ENTRY_LIMIT')
    digest = hashlib.sha256()
    for p in sorted(rows, key=lambda x: PurePosixPath(x).parts):
        if rows[p]['type'] == 'file':
            digest.update(p.encode() + b'\0' + bytes.fromhex(rows[p]['sha256']))
    need(digest.hexdigest() == TREE_PIN and rows['bin/go']['sha256'] == GO_PIN, 'MISMATCH')
    if expected is not None:
        need(rows.keys() == expected.keys(), 'CHANGED')
    return rows, {'files': files, 'directories': dirs, 'content_bytes': total, 'allocated_bytes': blocks,
                  'tree_sha256': digest.hexdigest(), 'go_sha256': rows['bin/go']['sha256']}


def run_key(value):
    need(re.fullmatch(r'[1-9][0-9]{0,19}-1-[0-9a-f]{40}', value) is not None, 'ARGUMENTS')
    return value

def root_name(key):
    return 'worktool-upstream-go-' + str(os.getuid()) + '-' + run_key(key)

def identity_binding(key):
    # Workflow verifies current parent, exact diff and all source bytes before this.
    if os.environ.get('GITHUB_ACTIONS') == 'true':
        need(os.environ.get('GITHUB_REPOSITORY') == 'ZhangIvan/worktool-open'
             and os.environ.get('GITHUB_REF') == 'refs/heads/worktool/synthetic-prototype'
             and os.environ.get('GITHUB_EVENT_NAME') == 'push'
             and os.environ.get('GITHUB_RUN_ATTEMPT') == '1'
             and key == os.environ.get('GITHUB_RUN_ID','') + '-1-' + os.environ.get('GITHUB_SHA','')
             and os.environ.get('AUTHORITY_SOURCE_VERIFIED') == '1', 'ARGUMENTS')
    fd = open_existing(os.path.abspath(__file__))
    try:
        before = os.fstat(fd)
        need(stat.S_ISREG(before.st_mode) and before.st_nlink == 1
             and before.st_size <= 256 * 1024, 'ARCHIVE_TYPE')
        data = bytearray()
        while len(data) < before.st_size:
            block = os.read(fd, min(BLOCK, before.st_size - len(data)))
            need(block, 'TRUNCATED')
            data.extend(block)
        need(not os.read(fd, 1) and ident(os.fstat(fd)) == ident(before), 'CHANGED')
        return hashlib.sha256(data).hexdigest()
    finally:
        os.close(fd)


def acquire(key, archive=None, private=False):
    key = run_key(key)
    helper_hash = identity_binding(key)
    base = scratch_base(private)
    name = root_name(key)
    root = fd = tool = None
    sink = None
    try:
        # EEXIST is a hard stop, including a retained uncertain previous attempt.
        os.mkdir(name, 0o700, dir_fd=base)
        root = os.open(name, FLAGS, dir_fd=base)
        rid = owned_dir(root)
        fs = os.fstatvfs(root)
        need(fs.f_frsize == 4096 and fs.f_bavail * fs.f_frsize >= MAX_BLOCKS
             and fs.f_favail >= MAX_ENTRIES + 4, 'FILE_LIMIT')
        if archive is None:
            fd = os.open('archive.gz', os.O_RDWR|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW, 0o600, dir_fd=root)
            download(fd)
        else:
            fd = open_existing(archive)
        arm(170)
        before = verify(fd)
        expected, summary = parse(fd, compressed_verified=True)
        need(ident(os.fstat(fd)) == before, 'CHANGED')
        os.mkdir('toolchain', 0o700, dir_fd=root)
        tool = os.open('toolchain', FLAGS, dir_fd=root)
        owned_dir(tool)
        os.lseek(fd, 0, os.SEEK_SET)
        sink = Materializer(tool, expected)
        second, second_summary = parse(fd, compressed_verified=True, sink=sink)
        need(second == expected and second_summary == summary and ident(os.fstat(fd)) == before, 'CHANGED')
        rows, stats = scan(tool, expected)
        # All archive, member, on-disk tree and binary pins passed before chmod.
        for p in sorted(EXECUTABLES):
            need(expected[p]['mode'] == 0o755, 'ARCHIVE_TYPE')
            d, n = parent_fd(tool, p)
            try:
                f = os.open(n, os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK, dir_fd=d)
                try:
                    need(record(os.fstat(f)) == rows[p]['identity'], 'CHANGED')
                    os.fchmod(f, 0o700)
                finally:
                    os.close(f)
            finally:
                os.close(d)
        rows, stats = scan(tool, expected, executable=True)
        if archive is None:
            ast = os.fstat(fd)
            need(record(os.stat('archive.gz',dir_fd=root,follow_symlinks=False)) == record(ast), 'CHANGED')
            os.unlink('archive.gz', dir_fd=root)
        need(owned_dir(root) == rid and set(os.listdir(root)) == {'toolchain'}, 'CHANGED')
        receipt = {'schema': 1, 'key': key, 'helper_sha256': helper_hash,
                   'root': list(rid), 'toolchain': list(owned_dir(tool)), 'rows': rows, 'stats': stats}
        raw = json.dumps(receipt, sort_keys=True, separators=(',', ':')).encode()
        need(len(raw) <= MAX_RECEIPT and stats['allocated_bytes'] + len(raw) + COMPRESSED_SIZE + 65536 <= MAX_BLOCKS, 'OUTPUT_LIMIT')
        out = os.open('receipt.json', os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW, 0o600, dir_fd=root)
        try:
            write_all(out, raw)
            os.fsync(out)
        finally:
            os.close(out)
        need(owned_dir(root) == rid and list(owned_dir(tool)) == receipt['toolchain'], 'CHANGED')
        need((os.stat(name, dir_fd=base, follow_symlinks=False).st_dev,
              os.stat(name, dir_fd=base, follow_symlinks=False).st_ino) == rid, 'CHANGED')
        return {'status': 'VERIFIED', 'key': key, 'root': ('/workspace/shared/' if private else '/tmp/') + name,
                'stats': stats, 'cleanup': 'REQUIRED_AFTER_USE'}
    finally:
        # No rollback deletion on failed or interrupted acquisition. No path is
        # exposed as executable unless a complete receipt and checks succeeded.
        if sink is not None and sink.current is not None:
            os.close(sink.current)
        for f in (tool, fd, root, base):
            if f is not None:
                os.close(f)


def cleanup(key, private=False):
    key = run_key(key)
    helper_hash = identity_binding(key)
    base = scratch_base(private)
    root = tool = None
    name = root_name(key)
    try:
        try:
            root = os.open(name, FLAGS, dir_fd=base)
        except FileNotFoundError:
            return {'status': 'ABSENT', 'cleanup': 'NO_STATE_PRESENT'}
        rid = owned_dir(root)
        need(set(os.listdir(root)) == {'toolchain','receipt.json'}, 'CLEANUP_UNCONFIRMED')
        f = os.open('receipt.json', os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK, dir_fd=root)
        try:
            st = os.fstat(f)
            need(stat.S_ISREG(st.st_mode) and st.st_uid == os.getuid() and st.st_nlink == 1
                 and stat.S_IMODE(st.st_mode) == 0o600 and st.st_size <= MAX_RECEIPT, 'CLEANUP_IDENTITY')
            raw = bytearray()
            while len(raw) < st.st_size:
                data = os.read(f, min(BLOCK, st.st_size-len(raw)))
                need(data, 'TRUNCATED')
                raw.extend(data)
            need(not os.read(f,1) and ident(os.fstat(f)) == ident(st), 'CHANGED')
            receipt = json.loads(raw)
        finally:
            os.close(f)
        need(receipt['schema'] == 1 and receipt['key'] == key and receipt['helper_sha256'] == helper_hash
             and receipt['root'] == list(rid), 'CLEANUP_IDENTITY')
        tool = os.open('toolchain', FLAGS, dir_fd=root)
        need(list(owned_dir(tool)) == receipt['toolchain'], 'CLEANUP_IDENTITY')
        rows, stats = scan(tool, executable=True)
        need(rows == receipt['rows'] and stats == receipt['stats'], 'CLEANUP_IDENTITY')
        # Complete exact tree and inode evidence before any delete; FD-relative
        # deletion only. Any unknown file or changed object stops and is retained.
        for p in sorted(rows, key=lambda x: (len(x.split('/')),x), reverse=True):
            d, n = parent_fd(tool, p)
            try:
                current = os.stat(n, dir_fd=d, follow_symlinks=False)
                saved = rows[p]['identity']
                if rows[p]['type'] == 'directory':
                    # Directory size/linkcount change as owned children are removed.
                    need([current.st_dev,current.st_ino] == saved[:2]
                         and stat.S_ISDIR(current.st_mode) and stat.S_IMODE(current.st_mode) == 0o700
                         and current.st_uid == os.getuid(), 'CLEANUP_IDENTITY')
                    os.rmdir(n, dir_fd=d)
                else:
                    need(record(current) == saved, 'CLEANUP_IDENTITY')
                    os.unlink(n, dir_fd=d)
            finally:
                os.close(d)
        need(not os.listdir(tool) and list(owned_dir(tool)) == receipt['toolchain'], 'CLEANUP_IDENTITY')
        need(record(os.stat('receipt.json', dir_fd=root, follow_symlinks=False)) == record(st), 'CLEANUP_IDENTITY')
        need(owned_dir(root) == rid and set(os.listdir(root)) == {'toolchain','receipt.json'}, 'CLEANUP_IDENTITY')
        os.rmdir('toolchain', dir_fd=root)
        os.unlink('receipt.json', dir_fd=root)
        need(not os.listdir(root), 'CLEANUP_IDENTITY')
        linked = os.stat(name, dir_fd=base, follow_symlinks=False)
        need((linked.st_dev,linked.st_ino) == rid, 'CLEANUP_IDENTITY')
        os.rmdir(name, dir_fd=base)
        try:
            os.stat(name, dir_fd=base, follow_symlinks=False)
        except FileNotFoundError:
            return {'status': 'CLEANED', 'cleanup': 'CONFIRMED'}
        raise Stop('CLEANUP_UNCONFIRMED')
    finally:
        for f in (tool,root,base):
            if f is not None:
                os.close(f)


def main():
    global DEADLINE
    os.umask(0o077)
    args = sys.argv[1:]
    cleaning = bool(args and args[0] in ('cleanup','cleanup-private'))
    DEADLINE = time.monotonic() + (45 if cleaning else 240)
    try:
        signal.signal(signal.SIGALRM, expired)
        arm(45 if cleaning else 240)
        if len(args) == 2 and args[0] == 'acquire':
            result = acquire(args[1])
        elif len(args) == 3 and args[0] == 'offline-private':
            need(os.environ.get('GITHUB_ACTIONS') != 'true', 'ARGUMENTS')
            result = acquire(args[1], args[2], private=True)
        elif len(args) == 2 and cleaning:
            need(args[0] != 'cleanup-private' or os.environ.get('GITHUB_ACTIONS') != 'true', 'ARGUMENTS')
            result = cleanup(args[1], args[0] == 'cleanup-private')
        else:
            raise Stop('ARGUMENTS')
        raw = json.dumps(result,sort_keys=True) + '\n'
        need(len(raw) <= 4096, 'OUTPUT_LIMIT')
        arm(5)
        sys.stdout.write(raw)
        sys.stdout.flush()
        signal.setitimer(signal.ITIMER_REAL, 0)
        return 0
    except Exception as exc:
        result = {'status': 'UNCONFIRMED', 'cleanup': 'RETAINED_IF_PRESENT',
                  'error': safe_error(exc, 'cleanup' if cleaning else 'acquisition'), 'helper_executed_go': False}
        try:
            arm(5)
            sys.stderr.write(json.dumps(result,sort_keys=True) + '\n')
            sys.stderr.flush()
        except Exception:
            pass
        return 3
    finally:
        try:
            signal.setitimer(signal.ITIMER_REAL, 0)
        except Exception:
            pass  # Never erase the primary failure; success disarms above.

if __name__ == '__main__':
    os._exit(main())
