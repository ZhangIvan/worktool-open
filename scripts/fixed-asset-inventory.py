#!/usr/bin/env python3
"""Bounded fixed-asset diagnostic only. Never extract or execute archive members."""
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

URL = 'https://github.com/actions/go-versions/releases/download/1.27.3-39437817176/go-1.27.3-linux-x64.tar.gz'
COMPRESSED_SIZE = 71607465
COMPRESSED_PIN = 'd0fbe16a6a03fe5fbf5577ab36f95f0725af587820dca8a78e6edd4eb4e19fd6'
TREE_PIN = 'da3e906bf3b01e50e398146823c6bf2b16a22951597834c5f4808ec38ca5e308'
GO_PIN = '5f5eb6d0b2fb6aa0bb5d5f8c5f1c944e0de955c925f0111af92a9d8f79680b5b'
MAX_COMPRESSED = 72 * 1024 * 1024
MAX_RAW = 1024 * 1024 * 1024
MAX_FILE = 128 * 1024 * 1024
MAX_OUTPUT = 8 * 1024 * 1024
MAX_ENTRIES = 25000
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
        need(len(newurl) <= 8192 and u.scheme == 'https' and u.hostname == 'release-assets.githubusercontent.com'
             and u.port in (None, 443) and not u.username and not u.password
             and not u.fragment and not any(ord(c) < 33 or ord(c) > 126 for c in newurl), 'REDIRECT_TARGET')
        need(code in (301, 302, 303, 307, 308), 'REDIRECT_STATUS')
        return urllib.request.Request(newurl, headers={'Accept-Encoding': 'identity'}, method='GET')

def download(fd, trace=None):
    trace = trace or Trace()
    trace.enter('tls_setup')
    budget = Budget(60)
    arm(60)
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
        need(response.headers.get('Content-Encoding', 'identity') == 'identity', 'HTTP_ENCODING')
        length = response.headers.get('Content-Length')
        need(length is None or length == str(COMPRESSED_SIZE), 'HTTP_SIZE')
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
    p = raw[2:] if raw.startswith('./') else raw
    if directory and p.endswith('/'):
        p = p[:-1]
    if raw == './' and directory:
        return ''
    parts = p.split('/')
    need(p and len(p.encode('utf-8')) <= 512 and len(parts) <= 32
         and all(x and x not in ('.', '..') for x in parts), 'PATH')
    return p

def parse(fd, compressed_verified=False):
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
        digest = hashlib.sha256()
        left = size
        while left:
            data = stream.exact(min(BLOCK, left))
            digest.update(data)
            left -= len(data)
        padding = (-size) % 512
        need(not any(stream.exact(padding)), 'TAR_PADDING')
        entries[p] = {'path': p, 'raw_path': raw, 'header_path': header_path, 'type': 'directory' if directory else 'file',
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
               'tar_members': members, 'pax_path_records': extensions, 'gnu_longname_records': longnames, 'root_container_raw_path': './' if root_seen else None, 'uncompressed_bytes': stream.raw}
    chunks = []
    total = 0
    for row in [summary] + ordered:
        stream.budget.check()
        line = (json.dumps(row, sort_keys=True, separators=(',', ':')) + '\n').encode('ascii')
        total += len(line)
        need(total <= MAX_OUTPUT, 'OUTPUT_LIMIT')
        chunks.append(line)
    return b''.join(chunks), status

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

def run(args, trace=None):
    trace = trace or Trace()
    trace.enter('storage')
    downloading = args in (['--download'], ['--download-private'])
    name = None
    base = d = fd = None
    file_identity = dir_identity = None
    raw = status = None
    try:
        need(downloading or len(args) == 2 and args[0] == '--archive', 'ARGUMENTS')
        if downloading:
            base = scratch_base(args == ['--download-private'])
            name = 'fixed-go-asset-' + secrets.token_hex(16)
            trace.cleanup = 'unconfirmed'
            os.mkdir(name, 0o700, dir_fd=base)  # Exclusive; no collision retries.
            d = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=base)
            st = os.fstat(d)
            dir_identity = (st.st_dev, st.st_ino)
            need(st.st_uid == os.getuid() and stat.S_IMODE(st.st_mode) == 0o700, 'SCRATCH_OWNER')
            fd = os.open('asset.gz', os.O_RDWR | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=d)
            fs = os.fstat(fd)
            file_identity = (fs.st_dev, fs.st_ino)
            trace.finish()
            download(fd, trace)
        else:
            fd = open_existing(args[1])
            trace.finish()
        arm(120)
        trace.enter('hashverify')
        before = verify(fd)
        trace.finish()
        trace.enter('parse')
        raw, status = parse(fd, compressed_verified=True)
        need(ident(os.fstat(fd)) == before, 'CHANGED')
        trace.finish()
    except Exception as exc:
        trace.primary = trace.failed(exc)
    finally:
        trace.enter('cleanup')
        try:
            arm(5)
            if fd is not None:
                os.close(fd)
            if d is not None:
                ds = os.stat(name, dir_fd=base, follow_symlinks=False)
                need((ds.st_dev, ds.st_ino) == dir_identity, 'CLEANUP_IDENTITY')
                if file_identity is not None:
                    fs = os.stat('asset.gz', dir_fd=d, follow_symlinks=False)
                    need((fs.st_dev, fs.st_ino) == file_identity, 'CLEANUP_IDENTITY')
                    os.unlink('asset.gz', dir_fd=d)
                    try:
                        os.stat('asset.gz', dir_fd=d, follow_symlinks=False)
                    except FileNotFoundError:
                        pass
                    else:
                        raise Stop('CLEANUP')
                os.rmdir(name, dir_fd=base)
                try:
                    os.stat(name, dir_fd=base, follow_symlinks=False)
                except FileNotFoundError:
                    pass
                else:
                    raise Stop('CLEANUP')
            elif name is not None:
                # Creation/open outcome cannot be proved owned: never broadly delete.
                raise Stop('CLEANUP_UNCONFIRMED')
        except Exception as exc:
            trace.cleanup_errors.append(trace.failed(exc))
        finally:
            for handle in (d, base):
                if handle is not None:
                    try:
                        os.close(handle)
                    except Exception as exc:
                        trace.cleanup_errors.append(trace.failed(exc))
        if trace.cleanup_errors:
            trace.cleanup = 'unconfirmed'
        else:
            trace.cleanup = 'completed' if name is not None else 'not_needed'
            trace.finish()
    if trace.primary or trace.cleanup_errors:
        raise Failure(trace)
    return raw, status

def main():
    global DEADLINE
    DEADLINE = time.monotonic() + 190
    trace = Trace()
    try:
        signal.signal(signal.SIGALRM, expired)
        arm(190)
        raw, status = run(sys.argv[1:], trace)
    except Failure:
        status = 'UNCONFIRMED'
        raw = (json.dumps(trace.report(), sort_keys=True) + '\n').encode('ascii')
    except Exception as exc:
        if trace.primary is None:
            trace.primary = trace.failed(exc)
        else:
            trace.secondary_errors.append(trace.failed(exc))
        status = 'UNCONFIRMED'
        raw = (json.dumps(trace.report(), sort_keys=True) + '\n').encode('ascii')
    trace.enter('output')
    try:
        arm(5)
        if status != 'UNCONFIRMED':
            first, separator, rest = raw.partition(b'\n')
            summary = json.loads(first)
            summary.update({'schema': 2, 'cleanup_status': trace.cleanup,
                            'completed_phases': list(trace.completed)})
            raw = (json.dumps(summary, sort_keys=True, separators=(',', ':')) + '\n').encode('ascii') + rest
            need(len(raw) <= MAX_OUTPUT, 'OUTPUT_LIMIT')
        sys.stdout.buffer.write(raw)
        sys.stdout.buffer.flush()
        signal.setitimer(signal.ITIMER_REAL, 0)
        trace.finish()
    except Exception as exc:
        # stdout may already be partial. Never retry it or declare inventory complete.
        output_error = trace.failed(exc)
        report = trace.report()
        report['output_error'] = output_error
        try:
            sys.stderr.write(json.dumps(report, sort_keys=True) + '\n')
            sys.stderr.flush()
        except Exception:
            pass  # No usable reporting sink remains; exit is still failure.
        status = 'UNCONFIRMED'
    return 0 if status == 'MATCH' else 2 if status == 'MISMATCH' else 3

if __name__ == '__main__':
    os._exit(main())  # Avoid implicit stdout reflush after a failed/partial write.
