"""Offline tests only: every would-be process is an injected fake executor."""
import contextlib
import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import signal
import sys
import tempfile
import time
import types
import unittest
from unittest.mock import patch

sys.dont_write_bytecode = True
path = Path(__file__).with_name('pg-registration-acceptance.py')
spec = importlib.util.spec_from_file_location('registration_acceptance_test_module', path)
m = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = m
spec.loader.exec_module(m)


def environment():
    return {'GITHUB_ACTIONS': 'true', 'RUNNER_ENVIRONMENT': 'github-hosted', 'RUNNER_OS': 'Linux',
            'RUNNER_ARCH': 'X64', 'GITHUB_REPOSITORY': 'ZhangIvan/worktool-open', 'GITHUB_SHA': 'a' * 40,
            'GITHUB_REF': 'refs/heads/worktool/synthetic-prototype', 'GITHUB_RUN_ID': '12345',
            'GITHUB_RUN_ATTEMPT': '1', 'GITHUB_EVENT_NAME': 'push',
            'GITHUB_WORKFLOW_REF': 'ZhangIvan/worktool-open/.github/workflows/go-registration-acceptance.yml@refs/heads/worktool/synthetic-prototype'}


def identity(env=None):
    return m.Identity.checked('ZhangIvan/worktool-open', 'a' * 40, 'refs/heads/worktool/synthetic-prototype',
                              '12345', '1', env or environment())


def fake_hba(admin):
    rules = []
    for typ, db, address, netmask, method in [
            ('local', ['all'], None, None, 'trust'),
            ('host', ['all'], '127.0.0.1', '255.255.255.255', 'scram-sha-256'),
            ('host', ['all'], '::1', 'ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff', 'scram-sha-256'),
            ('local', ['replication'], None, None, 'trust'),
            ('host', ['replication'], '127.0.0.1', '255.255.255.255', 'scram-sha-256'),
            ('host', ['replication'], '::1', 'ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff', 'scram-sha-256'),
            ('host', ['all'], 'all', None, 'scram-sha-256')]:
        n = len(rules) + 1
        rules.append({'rule_number': n, 'file_name': m.HBA_PATH, 'line_number': n + 100, 'type': typ,
                      'database': db, 'users': ['all'], 'address': address, 'netmask': netmask,
                      'auth_method': method, 'options': None, 'error': None})
    return {'session_user': admin, 'current_user': admin, 'database': 'postgres', 'hba_file': m.HBA_PATH,
        'config_load_epoch_us': 1793876402000000, 'postmaster_start_epoch_us': 1793876401000000,
        'checked_at_epoch_us': 1793876403000000, 'file_mtime_epoch_us': 1793876400000000,
        'file_size': 5800, 'file_is_directory': False, 'rule_count': len(rules), 'rules': rules}


class FakeEvidence:
    def load_manifest(self, path):
        return {}

    def source_digest(self, root, manifest):
        return m.tree_digest(m.tree_files(root))

    def validate_events(self, raw, manifest):
        if raw != b'SYNTHETIC_OFFLINE_EVENTS':
            raise ValueError('hidden raw failure')

    def validate_run(self, raw, manifest, binding, expected):
        self.validate_events(raw, manifest)
        if (binding['cleanup'] != 'CONFIRMED' or binding['exit_code'] != 0 or
                binding['source_manifest_sha256'] != expected or binding['go_invocations'] != 1):
            raise ValueError('bad synthetic binding')
        return {'status': 'FAKE_EXECUTOR_PROTOCOL_ONLY', 'go_invocations': 1}


class FakeProvision:
    SOURCE_PINS = {'source.sql': 'a' * 64}
    canonical = staticmethod(m.canonical)
    def sanitized_catalog_proof(self, receipt):
        public = receipt.public_evidence
        return {'format': 'worktool-registration-catalog-proof-v1', 'proof_model': 'OFFLINE_FAKE_ONLY',
            'constraint_semantic_normalization': 'NOT_PROVEN', 'objects': [{'kind': 'identity'}],
            **{key: public[key] for key in ('sql_sources', 'sql_manifest_sha256', 'install_receipt_sha256', 'catalog_observation_sha256', 'execution')}}

    def owner_admission_query(self, bundle, database, metadata):
        return 'SELECT synthetic_isolated_owner_session_v1();'

    def load_and_verify_sources(self, root):
        return None

    def provision(self, executor, bundle, database, bootstrap, passwords):
        for phase, role, db in [('roles', bootstrap, 'postgres'), ('database', bootstrap, 'postgres'),
                                 ('install', m.ROLE_NAMES[0], database), ('catalog', m.ROLE_NAMES[0], database)]:
            step = types.SimpleNamespace(phase=phase, login_role=role, database=db,
                                         sql='SELECT synthetic_' + phase + ';', query=False, timeout_seconds=30)
            executor.execute(step)
        private = {'format': 'FAKE_CATALOG_FOR_OFFLINE_PROTOCOL_ONLY'}
        public = {'format': 'worktool-registration-install-summary-v1', 'status': 'VERIFIED_TRUSTED_INSTALLATION_PROVENANCE',
            'sql_manifest_sha256': 'a' * 64, 'catalog_observation_sha256': 'b' * 64, 'install_receipt_sha256': m.sha(m.canonical(private)),
            'table_count': 37, 'function_count': 20, 'trigger_count': 24, 'constraint_count': 354,
            'constraint_semantic_normalization': 'NOT_PROVEN', 'acceptance_tests': 'NOT_RUN',
            'execution': [{'phase': 'catalog', 'login_role': m.ROLE_NAMES[0], 'database': database, 'sql_sha256': 'c' * 64, 'acknowledged': True}],
            'sql_sources': dict(self.SOURCE_PINS)}
        return types.SimpleNamespace(private_metadata={'database_oid': 100, 'schema_oid': 101, 'consumer_oid': 102,
            'issuer_oid': 103, 'boundary_sha256': 'b' * 64, 'registry_sha256': 'c' * 64,
            'routine_definition_sha256': 'd' * 64, 'bootstrap_role': bootstrap, 'bootstrap_oid': 10,
            'activity_guard_oid': 104, 'admission_schema_oid': 105, 'admission_function_oid': 106,
            'admission_function_definition_sha256': 'e' * 64, 'admission_sha256': 'f' * 64}, public_evidence=public, private_catalog_receipt=private)


class FakeExecutor:
    def __init__(self):
        self.calls = []
        self.container = None
        self.supervisor = None
        self.fail = None
        self.fail_after = False
        self.output = b'SYNTHETIC_OFFLINE_EVENTS'
        self.removals = 0
        self.invocations = 0
        self.inspect_mutation = None
        self.hba_mutation = None
        self.hba_after_mutation = None
        self.bad_password_result = None
        self.hba_canonical_path = m.HBA_PATH
        self.settings_mutation = None
        self.activity_output = b'[{"activity_admitted":true}]\n'

    def phase(self, args):
        if args[0] != m.DOCKER:
            return 'fixture' if 'test2json' in args else ('modules' if 'verify' in args else 'build')
        if args[1] == 'exec':
            if args[3] == 'pg_isready':
                return 'ready'
            if args[3] == 'readlink':
                return 'hba_path' if args[-1] == m.HBA_PATH else 'wal'
            data = self.last_data
            if b'pg_hba_file_rules' in data:
                return 'hba_before' if self.supervisor.stage == 'auth_hba_before' else 'hba_after'
            if self.supervisor.stage == 'auth_wrong_password':
                return 'bad_password'
            if self.supervisor.stage == 'bootstrap_close':
                return 'activity'
            if b'json_build_object' in data:
                return 'settings'
            if b'pg_stat_activity' in data or b'isolated_owner_session_v1' in data:
                return 'activity'
            return 'sql_' + self.supervisor.stage.removeprefix('provision_')
        if args[1] == 'image':
            return 'image'
        return args[1]

    def call(self, args, *, data=b'', env=None, cwd=None, timeout=30):
        self.last_data = data
        self.calls.append((list(args), data, env, cwd, timeout))
        phase = self.phase(args)
        if self.fail == phase and not self.fail_after:
            raise m.Fault('SUBPROCESS_TIMEOUT', True)
        output = b''
        if phase == 'modules':
            output = b'all modules verified\n'
        elif phase == 'build':
            m.private_file(Path(args[args.index('-o') + 1]), b'OFFLINE_NOT_EXECUTABLE')
        elif phase == 'fixture':
            self.invocations += 1
            output = self.output
        elif phase == 'ps':
            output = (self.container['id'] + '\n').encode() if self.container else b''
        elif phase == 'image':
            output = m.canonical({'id': 'sha256:' + 'd' * 64, 'digests': [m.IMAGE], 'architecture': 'amd64', 'os': 'linux'})
        elif phase == 'create':
            self.container = {'id': 'c' * 64, 'name': '/' + self.supervisor.identity.name,
                'owner': self.supervisor.marker['owner'], 'identity': self.supervisor.identity.key,
                'image': m.IMAGE, 'image_id': 'sha256:' + 'd' * 64, 'mounts': [],
                'tmpfs': {'/var/lib/postgresql': 'rw,nosuid,nodev,size=2147483648'},
                'memory': 1 << 30, 'swap': 1 << 30, 'cpus': 2_000_000_000, 'shm': 64 << 20,
                'privileged': False, 'network': 'bridge', 'pid': '', 'ipc': 'private',
                'cap_add': None, 'cap_drop': ['CAP_NET_RAW'], 'ports': {'5432/tcp': None},
                'running': False}
            output = (self.container['id'] + '\n').encode()
        elif phase == 'start':
            self.container['running'] = True
            self.container['ports'] = {'5432/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '54321'}]}
        elif phase == 'inspect':
            value = dict(self.container)
            if self.inspect_mutation:
                value.update(self.inspect_mutation)
            output = m.canonical(value)
        elif phase in ('hba_before', 'hba_after'):
            snapshot = fake_hba(self.supervisor.admin)
            if self.hba_mutation:
                self.hba_mutation(snapshot)
            if phase == 'hba_after' and self.hba_after_mutation:
                self.hba_after_mutation(snapshot)
            output = m.canonical(snapshot)
        elif phase == 'bad_password':
            if self.bad_password_result is not None:
                return self.bad_password_result
            message = ('psql: error: connection to server at "127.0.0.1", port 5432 failed: FATAL:  password authentication failed for user "' + self.supervisor.admin + '"\n').encode()
            return m.Result(2, b'', message)
        elif phase == 'settings':
            settings = {'version': '180006', 'version_string': 'PostgreSQL 18.6 synthetic', 'connections': '10',
                'reserved': '0', 'super_reserved': '3', 'messages': 'C', 'statement': '5s', 'lock': '1s', 'idle': '5s',
                'data': '/var/lib/postgresql/18/docker', 'shared_preload': '', 'session_preload': '', 'local_preload': '',
                'archive_mode': 'off', 'summarize_wal': 'off', 'io_method': 'worker', 'io_workers': '3'}
            if self.settings_mutation:
                self.settings_mutation(settings)
            output = m.canonical(settings)
        elif phase == 'hba_path':
            output = self.hba_canonical_path.encode() + b'\n'
        elif phase == 'wal':
            output = b'/var/lib/postgresql/18/docker/pg_wal\n'
        elif phase == 'activity':
            output = self.activity_output
        elif phase == 'rm':
            self.removals += 1
            self.container = None
        if self.fail == phase and self.fail_after:
            raise m.Fault('SUBPROCESS_TIMEOUT', True)
        return m.Result(0, output)


class SupervisorTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='registration-supervisor-offline-')
        self.root = Path(self.tmp.name)
        self.source = self.root / 'source'
        self.source.mkdir(mode=0o700)
        (self.source / 'scripts').mkdir(mode=0o700)
        (self.source / 'backend-go/internal/store/pg').mkdir(parents=True, mode=0o700)
        m.private_file(self.source / 'scripts/pg-registration-cases.json', b'{}')
        m.private_file(self.source / 'scripts/pg-registration-acceptance.py', b'OFFLINE_MODULE_LOCATION_BINDING')
        m.private_file(self.source / 'frozen-source.txt', b'FROZEN_SYNTHETIC_SOURCE')
        self.go_root = self.root / 'go'
        (self.go_root / 'bin').mkdir(parents=True, mode=0o700)
        m.private_file(self.go_root / 'bin/go', b'OFFLINE_FAKE_GO_NO_EXECUTE')
        m.private_file(self.go_root / 'VERSION', b'go1.27.3\ntime 2026-11-05\n')
        self.cache = self.root / 'module-cache'
        self.cache.mkdir(mode=0o700)
        self.source_pin = self.root / 'source-pin.json'
        self.toolchain_pin = self.root / 'toolchain-pin.json'
        source_bytes = m.make_pin(self.source)
        toolchain_bytes = m.make_pin(self.go_root, toolchain=True)
        m.private_file(self.source_pin, source_bytes)
        m.private_file(self.toolchain_pin, toolchain_bytes)
        self.config = m.RunConfig(identity(), self.root, self.source, self.source_pin, m.sha(source_bytes),
            self.go_root / 'bin/go', self.toolchain_pin, m.sha(toolchain_bytes), self.cache, time.monotonic_ns() + 839_000_000_000)
        self.executor = FakeExecutor()
        self.s = m.Supervisor(self.config, self.executor, FakeProvision(), FakeEvidence(), sleep=lambda _: None)
        self.executor.supervisor = self.s

    def tearDown(self):
        self.tmp.cleanup()

    def run_fake(self):
        with patch.object(m, '__file__', str(self.source / 'scripts/pg-registration-acceptance.py')):
            return self.s.run(handle_signals=False)

    def test_cross_module_real_plan_receipt_envelope_events_and_cleanup(self):
        # Real implementation helpers, source contract and exact-leaf parser;
        # every external operation, catalog row and event is fabricated here.
        actual_root = path.resolve().parents[1]
        provision_tests = m.load_helper('test_pg_registration_provision.py')
        evidence_tests = m.load_helper('test_pg_registration_evidence.py')
        provision = m.load_helper('pg-registration-provision.py')
        evidence = m.load_helper('pg-registration-evidence.py')
        class IntegratedExecutor(FakeExecutor):
            def phase(self, args):
                if args[0] == m.DOCKER and args[1] == 'exec' and '-i' in args and self.supervisor.stage.startswith('provision_'):
                    return 'sql_' + self.supervisor.stage.removeprefix('provision_')
                return super().phase(args)
            def call(self, args, **kwargs):
                result = super().call(args, **kwargs)
                current = self.phase(args)
                if current == 'sql_bootstrap_preflight':
                    rows = [dict(version=180006, session_user=self.supervisor.admin, current_user=self.supervisor.admin,
                                 database_absent=True, roles_absent=True, activity_admitted=True, bootstrap_oid=10, bootstrap_superuser=True, event_triggers=0)]
                    return m.Result(0, m.canonical(rows))
                if current == 'sql_bootstrap_helper':
                    return m.Result(0, m.canonical([{'guard_password_is_null': True}]))
                if current == 'sql_catalog_readback':
                    rows = provision_tests.fake_catalog(self.supervisor.bundle)
                    for row in rows:
                        for key, value in row.items():
                            if value == provision_tests.ADMIN:
                                row[key] = self.supervisor.admin
                    next(row for row in rows if row['kind'] == 'identity')['database'] = self.supervisor.database
                    return m.Result(0, m.canonical(rows))
                return result
        source_bytes = m.make_pin(actual_root)
        self.source_pin.write_bytes(source_bytes)
        self.config.source_root = actual_root
        self.config.source_pin_sha256 = m.sha(source_bytes)
        executor = IntegratedExecutor()
        supervisor = m.Supervisor(self.config, executor, provision, evidence, sleep=lambda _: None)
        executor.supervisor = supervisor
        manifest = evidence.load_manifest(actual_root / 'scripts/pg-registration-cases.json')
        executor.output = evidence_tests.encode(evidence_tests.complete(manifest))
        envelope_seen = []
        original_write = supervisor.write_envelope
        def capture_envelope():
            original_write()
            envelope_seen.append(json.loads(m.read_regular(supervisor.folder / 'envelope.json', 32768, private=True)))
        supervisor.write_envelope = capture_envelope
        with patch.object(m.subprocess, 'Popen', side_effect=AssertionError('MUST_NOT_LAUNCH')):
            result = supervisor.run(handle_signals=False)
        self.assertEqual(result['status'], 'PASS_FOCUSED_ISOLATED_SCOPE', result)
        self.assertEqual(result['evidence']['leaves'], manifest['leaves'])
        self.assertEqual(len(result['installation']['execution']), 19)
        self.assertEqual(set(result['provision_diagnostics']['phases']), set(m.PROVISION_PHASES))
        self.assertEqual(result['preflight_diagnostics']['classification'], 'VALUE_MATCH')
        self.assertEqual(set(result['preflight_diagnostics']['checks'].values()), {'MATCH'})
        proof_bytes = (supervisor.folder / 'catalog-proof.json').read_bytes()
        self.assertEqual(m.sha(proof_bytes), result['catalog_proof_sha256'])
        self.assertGreater(len(json.loads(proof_bytes)['objects']), 1000)
        self.assertEqual(executor.invocations, 1)
        self.assertEqual(executor.removals, 1)
        self.assertEqual(result['cleanup'], 'CONFIRMED')
        supervisor.verify_standalone_cleanup_source()
        self.assertEqual(envelope_seen[0]['source_manifest_sha256'], result['source_sha256'])
        self.assertEqual(envelope_seen[0]['executable_sha256'], result['executable_sha256'])
        self.assertNotIn('owner_dsn', json.dumps(result))
        self.assertFalse((supervisor.folder / 'envelope.json').exists())

    def make_stage_source(self):
        cache = self.root / 'shared-cache'
        cache.mkdir(mode=0o700)
        for name in ('bin/go', 'pkg/tool/linux_amd64/compile', 'pkg/tool/linux_amd64/link',
                     'pkg/tool/linux_amd64/asm', 'pkg/tool/linux_amd64/vet'):
            target = cache / name
            target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o700)
            with os.fdopen(fd, 'wb') as output:
                output.write(b'FAKE_OFFLINE_NEVER_EXECUTED_' + name.encode())
        (cache / 'src/cmd/test2json').mkdir(mode=0o700, parents=True)
        m.private_file(cache / 'src/cmd/test2json/main.go', b'package main // FAKE_OFFLINE_NEVER_COMPILED')
        m.private_file(cache / 'VERSION', b'go1.27.3\ntime 2026-11-05\n')
        m.private_file(cache / 'LICENSE', b'FAKE_PUBLIC_SOURCE_FIXTURE')
        (cache / 'empty').mkdir(mode=0o700)
        return cache

    def test_private_toolchain_stage_copies_bytes_and_exec_bits_without_launch(self):
        cache = self.make_stage_source()
        destination = self.root / 'private-toolchain'
        before = m.make_pin(cache, toolchain=True)
        with patch.object(m.subprocess, 'Popen', side_effect=AssertionError('MUST_NOT_LAUNCH')):
            result = m.stage_toolchain(cache, destination)
        self.assertEqual(result['status'], 'TOOLCHAIN_STAGED_NOT_EXECUTED')
        self.assertEqual(m.make_pin(destination, toolchain=True), before)
        self.assertEqual(m.make_pin(cache, toolchain=True), before)
        self.assertEqual((destination / 'bin/go').stat().st_mode & 0o777, 0o700)
        self.assertEqual((destination / 'LICENSE').stat().st_mode & 0o777, 0o600)
        self.assertEqual(destination.stat().st_mode & 0o777, 0o700)
        self.assertTrue((destination / 'empty').is_dir())
        self.assertEqual(self.executor.calls, [])

    def test_shared_writable_cache_is_read_only_intake_not_executable_root(self):
        cache = self.make_stage_source()
        destination = self.root / 'private-toolchain'
        original = m.Path.lstat
        def writable_cache(path):
            info = original(path)
            if path == cache:
                fields = list(info)
                fields[0] = (info.st_mode & ~0o777) | 0o775
                return os.stat_result(fields)
            return info
        with patch.object(m.Path, 'lstat', writable_cache):
            with self.assertRaises(m.Fault) as caught:
                m.make_pin(cache, toolchain=True)
            self.assertEqual(caught.exception.code, 'PATH_MODE_REFUSED')
            result = m.stage_toolchain(cache, destination)
            self.assertEqual(result['status'], 'TOOLCHAIN_STAGED_NOT_EXECUTED')
            with self.assertRaises(m.Fault) as unchanged:
                m.checked_directory(cache, allow_root_owner=True)
            self.assertEqual(unchanged.exception.code, 'PATH_MODE_REFUSED')
        self.assertEqual(m.tree_digest(m.tree_files(destination, toolchain=True)), result['tree_sha256'])

    def test_staging_refuses_existing_destination_without_mutation(self):
        cache = self.make_stage_source()
        destination = self.root / 'foreign-destination'
        destination.mkdir(mode=0o700)
        m.private_file(destination / 'keep', b'UNCHANGED')
        with self.assertRaises(m.Fault) as caught:
            m.stage_toolchain(cache, destination)
        self.assertEqual(caught.exception.code, 'TOOLCHAIN_STAGE_EXISTS')
        self.assertEqual((destination / 'keep').read_bytes(), b'UNCHANGED')
        self.assertEqual(sorted(p.name for p in destination.iterdir()), ['keep'])

    def test_staging_refuses_root_and_nested_symlinks(self):
        cache = self.make_stage_source()
        alias = self.root / 'cache-alias'
        alias.symlink_to(cache, target_is_directory=True)
        destination = self.root / 'private-toolchain'
        with self.assertRaises(m.Fault) as root_error:
            m.stage_toolchain(alias, destination)
        self.assertEqual(root_error.exception.code, 'PATH_NOT_CANONICAL')
        (cache / 'nested-link').symlink_to(cache / 'VERSION')
        with self.assertRaises(m.Fault):
            m.stage_toolchain(cache, destination)
        self.assertFalse(destination.exists())

    def test_staging_refuses_hardlinked_files(self):
        cache = self.make_stage_source()
        os.link(cache / 'VERSION', cache / 'VERSION-link')
        with self.assertRaises(m.Fault) as caught:
            m.stage_toolchain(cache, self.root / 'private-toolchain')
        self.assertEqual(caught.exception.code, 'TOOLCHAIN_STAGE_INVALID')
        self.assertFalse((self.root / 'private-toolchain').exists())

    def test_staging_refuses_foreign_owner_without_relaxing_other_roots(self):
        cache = self.make_stage_source()
        original = m.Path.lstat
        def foreign_owner(path):
            info = original(path)
            if path == cache:
                fields = list(info)
                fields[4] = 999999
                return os.stat_result(fields)
            return info
        with patch.object(m.Path, 'lstat', foreign_owner), self.assertRaises(m.Fault) as caught:
            m.stage_toolchain(cache, self.root / 'private-toolchain')
        self.assertEqual(caught.exception.code, 'PATH_OWNER_REFUSED')

    def test_staging_rejects_source_change_and_leaves_unadoptable_partial_copy(self):
        cache = self.make_stage_source()
        destination = self.root / 'private-toolchain'
        original = m._stage_read_file
        changed = [False]
        def mutate_after_read(path, deadline, expected=None, output=None):
            result = original(path, deadline, expected=expected, output=output)
            if output is not None and not changed[0]:
                (cache / 'VERSION').write_bytes(b'go1.27.3\ntime CHANGED_SOURCE\n')
                changed[0] = True
            return result
        with patch.object(m, '_stage_read_file', mutate_after_read), self.assertRaises(m.Fault) as caught:
            m.stage_toolchain(cache, destination)
        self.assertEqual(caught.exception.code, 'TOOLCHAIN_STAGE_CHANGED')
        self.assertTrue(destination.is_dir())
        with self.assertRaises(m.Fault) as repeated:
            m.stage_toolchain(cache, destination)
        self.assertEqual(repeated.exception.code, 'TOOLCHAIN_STAGE_EXISTS')

    def test_staging_rejects_destination_byte_drift(self):
        cache = self.make_stage_source()
        destination = self.root / 'private-toolchain'
        original = m._stage_inventory
        def mutate_before_target_inventory(root, deadline):
            if root == destination:
                (destination / 'bin/go').write_bytes(b'ALTERED_COPY')
            return original(root, deadline)
        with patch.object(m, '_stage_inventory', mutate_before_target_inventory), self.assertRaises(m.Fault) as caught:
            m.stage_toolchain(cache, destination)
        self.assertEqual(caught.exception.code, 'TOOLCHAIN_STAGE_CHANGED')

    def test_staging_traversal_error_never_becomes_partial_success(self):
        cache = self.make_stage_source()
        destination = self.root / 'private-toolchain'
        def failing_walk(root, *, followlinks, onerror):
            self.assertFalse(followlinks)
            onerror(PermissionError('private unreadable subtree detail'))
            return iter(())
        with patch.object(m.os, 'walk', failing_walk), self.assertRaises(m.Fault) as caught:
            m.stage_toolchain(cache, destination)
        self.assertEqual(caught.exception.code, 'TOOLCHAIN_STAGE_INVALID')
        self.assertNotIn('private', str(caught.exception))
        self.assertFalse(destination.exists())

    def test_staging_deadline_expires_before_any_copy(self):
        cache = self.make_stage_source()
        destination = self.root / 'private-toolchain'
        with patch.object(m.time, 'monotonic', side_effect=[0, 61]), self.assertRaises(m.Fault) as caught:
            m.stage_toolchain(cache, destination)
        self.assertEqual(caught.exception.code, 'TOOLCHAIN_STAGE_TIMEOUT')
        self.assertFalse(destination.exists())

    def test_staging_bounds_and_wrong_version_rejected(self):
        cache = self.make_stage_source()
        (cache / 'VERSION').write_bytes(b'go1.28.0\n')
        with self.assertRaises(m.Fault) as caught:
            m.stage_toolchain(cache, self.root / 'private-toolchain')
        self.assertEqual(caught.exception.code, 'TOOLCHAIN_STAGE_INVALID')
        original = m.Path.lstat
        def oversized_file(path):
            info = original(path)
            if path == cache / 'LICENSE':
                fields = list(info)
                fields[6] = (128 << 20) + 1
                return os.stat_result(fields)
            return info
        with patch.object(m.Path, 'lstat', oversized_file), self.assertRaises(m.Fault):
            m.stage_toolchain(cache, self.root / 'private-toolchain')
        self.assertFalse((self.root / 'private-toolchain').exists())

    def test_default_and_noop_never_launch(self):
        for argv in ([], ['noop']):
            with self.subTest(argv=argv), contextlib.redirect_stdout(io.StringIO()), patch.object(m.subprocess, 'Popen', side_effect=AssertionError('MUST_NOT_LAUNCH')):
                self.assertEqual(m.main(argv, executor=self.executor, environ={}), 0)
                self.assertEqual(self.executor.calls, [])

    def test_import_never_launches(self):
        with patch.object(m.subprocess, 'Popen', side_effect=AssertionError('MUST_NOT_LAUNCH')):
            other_spec = importlib.util.spec_from_file_location('inert_supervisor_import', path)
            other = importlib.util.module_from_spec(other_spec)
            sys.modules[other_spec.name] = other
            other_spec.loader.exec_module(other)

    def test_malformed_identity_no_launch(self):
        for key, bad in [('GITHUB_ACTIONS', 'false'), ('RUNNER_ENVIRONMENT', 'self-hosted'), ('RUNNER_ARCH', 'ARM64'),
                         ('GITHUB_REPOSITORY', 'other/repo'), ('GITHUB_SHA', 'b' * 40), ('GITHUB_RUN_ATTEMPT', '2'), ('GITHUB_EVENT_NAME', 'workflow_dispatch'), ('GITHUB_WORKFLOW_REF', 'foreign')]:
            env = environment()
            env[key] = bad
            with self.subTest(key=key), self.assertRaises(m.Fault):
                identity(env)
        self.assertEqual(self.executor.calls, [])

    def test_rerun_attempt_and_bad_ref_refused(self):
        for ref, attempt in [('refs/heads/main', '1'), ('refs/heads/worktool/synthetic-prototype', '2')]:
            with self.assertRaises(m.Fault):
                m.Identity.checked('ZhangIvan/worktool-open', 'a' * 40, ref, '12345', attempt, environment())
        self.assertEqual(self.executor.calls, [])

    def test_frozen_source_mutation_prevents_any_process(self):
        (self.source / 'frozen-source.txt').write_bytes(b'CHANGED')
        result = self.run_fake()
        self.assertEqual(result['failure_code'], 'SOURCE_PIN_MISMATCH')
        self.assertEqual(self.executor.calls, [])

    def test_toolchain_mutation_prevents_any_process(self):
        (self.go_root / 'bin/go').write_bytes(b'CHANGED')
        result = self.run_fake()
        self.assertEqual(result['failure_code'], 'TOOLCHAIN_PIN_MISMATCH')
        self.assertEqual(self.executor.calls, [])

    def test_foreign_directory_is_not_adopted_or_deleted(self):
        self.s.folder.mkdir(mode=0o700)
        m.private_file(self.s.folder / 'foreign', b'KEEP')
        result = self.run_fake()
        self.assertEqual(result['failure_code'], 'RUN_ALREADY_STARTED')
        self.assertEqual((self.s.folder / 'foreign').read_bytes(), b'KEEP')
        self.assertEqual(self.executor.calls, [])

    def test_foreign_container_preflight_is_not_removed(self):
        self.executor.container = {'id': 'f' * 64}
        result = self.run_fake()
        self.assertEqual(result['failure_code'], 'FOREIGN_CONTAINER_PRESENT')
        self.assertEqual(self.executor.removals, 0)
        self.assertFalse(self.s.folder.exists())

    def test_complete_fake_lifecycle_is_one_run_and_idempotent_cleanup(self):
        result = self.run_fake()
        self.assertEqual(result['status'], 'PASS_FOCUSED_ISOLATED_SCOPE')
        self.assertEqual(self.executor.invocations, 1)
        self.assertEqual(self.executor.removals, 1)
        self.assertEqual(self.s.cleanup(), 'CONFIRMED')
        self.assertEqual(self.executor.removals, 1)
        self.assertEqual(sorted(p.name for p in self.s.folder.iterdir()), ['catalog-proof.json', 'outcome.json', 'private-install-receipt.json', 'run-marker.json'])
        self.assertEqual(self.s.folder.stat().st_mode & 0o777, 0o700)
        for p in self.s.folder.iterdir():
            self.assertEqual(p.stat().st_mode & 0o777, 0o600)
        second = self.run_fake()
        self.assertEqual(second['failure_code'], 'RUN_ALREADY_STARTED')
        self.assertEqual(self.executor.invocations, 1)

    def test_all_process_stages_fail_closed_no_fixture_retry(self):
        stages = ['modules', 'build', 'pull', 'image', 'create', 'start', 'inspect', 'ready', 'settings', 'wal', 'hba_path', 'hba_before', 'bad_password', 'hba_after',
                  'sql_roles', 'sql_database', 'sql_install', 'sql_catalog', 'activity', 'fixture']
        for stage in stages:
            with self.subTest(stage=stage):
                # A distinct isolated test fixture for every fault, not a retry.
                case = SupervisorTests('runTest')
                case.setUp()
                try:
                    case.executor.fail = stage
                    result = case.run_fake()
                    self.assertEqual(result['status'], 'FAIL')
                    self.assertNotEqual(result['run'], 'PASS')
                    self.assertLessEqual(case.executor.invocations, 1)
                    self.assertLessEqual(case.executor.removals, 1)
                    self.assertIn(result['cleanup'], ('CONFIRMED', 'UNCONFIRMED'))
                finally:
                    case.tearDown()

    def test_uncertain_create_absence_is_not_cleanup_proof(self):
        self.executor.fail = 'create'
        result = self.run_fake()
        self.assertEqual(result['cleanup'], 'UNCONFIRMED')
        self.assertEqual(self.executor.removals, 0)
        marker = self.s.authenticated_marker()
        self.assertEqual(marker['create_state'], 'SENT')
        with self.assertRaises(m.Fault):
            self.s.cleanup()

    def test_uncertain_create_observed_owned_resource_can_be_removed(self):
        self.executor.fail, self.executor.fail_after = 'create', True
        result = self.run_fake()
        self.assertEqual(result['run'], 'FAIL')
        self.assertEqual(result['cleanup'], 'CONFIRMED')
        self.assertEqual(self.executor.removals, 1)
        self.assertEqual(self.s.authenticated_marker()['create_state'], 'OBSERVED')

    def test_remove_timeout_requires_post_operation_inspection(self):
        self.executor.fail, self.executor.fail_after = 'rm', True
        result = self.run_fake()
        self.assertEqual(result['cleanup'], 'CONFIRMED')
        phases = [x[0][1] for x in self.executor.calls if x[0][0] == m.DOCKER]
        self.assertEqual(phases[-2:], ['rm', 'ps'])
        self.assertEqual(self.executor.removals, 1)

    def test_cleanup_denied_preserves_run_and_cleanup_separately(self):
        self.executor.fail = 'rm'
        result = self.run_fake()
        self.assertEqual(result['status'], 'FAIL')
        self.assertEqual(result['run'], 'CANDIDATE_PASS')
        self.assertEqual(result['cleanup'], 'UNCONFIRMED')
        self.assertIsNotNone(self.executor.container)
        self.executor.fail = None
        self.assertEqual(self.s.cleanup(), 'CONFIRMED')
        self.assertEqual(self.executor.removals, 1)
        self.assertEqual(self.executor.invocations, 1)

    def test_standalone_cleanup_source_drift_refused_before_docker(self):
        self.assertEqual(self.run_fake()['status'], 'PASS_FOCUSED_ISOLATED_SCOPE')
        before = len(self.executor.calls)
        with patch.object(m, '__file__', str(self.source / 'scripts/pg-registration-acceptance.py')):
            self.s.verify_standalone_cleanup_source()
            (self.source / 'frozen-source.txt').write_bytes(b'CHANGED_AFTER_RUN')
            with self.assertRaises(m.Fault):
                self.s.verify_standalone_cleanup_source()
        self.assertEqual(len(self.executor.calls), before)
        # The already loaded finally-cleanup path remains available on drift.
        self.assertEqual(self.s.cleanup(), 'CONFIRMED')

    def test_cleanup_owner_mismatch_never_deletes_container(self):
        self.executor.inspect_mutation = {'owner': 'f' * 64}
        result = self.run_fake()
        self.assertEqual(result['status'], 'FAIL')
        self.assertEqual(result['cleanup'], 'UNCONFIRMED')
        self.assertEqual(self.executor.removals, 0)

    def test_each_resource_mismatch_stops_before_sql(self):
        for key, bad in [('memory', 2 << 30), ('swap', 2 << 30), ('cpus', 3_000_000_000),
                         ('shm', 128 << 20), ('privileged', True), ('network', 'host'), ('pid', 'host'),
                         ('ipc', 'host'), ('cap_add', ['SYS_ADMIN']), ('cap_drop', []),
                         ('tmpfs', {}), ('mounts', [{'Type': 'bind', 'Destination': '/var/lib/postgresql'}]),
                         ('ports', {'5432/tcp': [{'HostIp': '0.0.0.0', 'HostPort': '54321'}]})]:
            with self.subTest(key=key):
                case = SupervisorTests('runTest')
                case.setUp()
                try:
                    case.executor.inspect_mutation = {key: bad}
                    result = case.run_fake()
                    self.assertEqual(result['failure_code'], 'RESOURCE_MISMATCH')
                    self.assertEqual(case.executor.invocations, 0)
                    self.assertFalse(any(b'json_build_object' in call[1] for call in case.executor.calls))
                finally:
                    case.tearDown()

    def test_no_secrets_in_arguments_or_child_environment_or_evidence(self):
        result = self.run_fake()
        secrets_used = set()
        for args, data, env, cwd, timeout in self.executor.calls:
            self.assertLessEqual(timeout, 300)
            if data:
                first = data.split(b'\n', 1)[0].decode()
                if m.HEX.fullmatch(first):
                    secrets_used.add(first)
            self.assertNotIn('Config.Env', ' '.join(args))
            self.assertFalse(env and any(k.startswith('PG') for k in env))
        for secret in secrets_used:
            self.assertNotIn(secret, json.dumps(result))
            self.assertNotIn(secret, (self.s.folder / 'outcome.json').read_text())
            for args, data, env, cwd, timeout in self.executor.calls:
                self.assertNotIn(secret, json.dumps(args))
                self.assertNotIn(secret, json.dumps(env))
        self.assertEqual(len(secrets_used), 3)  # bootstrap, one wrong-password probe, actual owner

    def test_rejected_catalog_proof_never_invokes_fixture_or_passes(self):
        self.s.provision_helper.sanitized_catalog_proof = lambda receipt: (_ for _ in ()).throw(ValueError('private-catalog-content'))
        result = self.run_fake()
        self.assertEqual(result['status'], 'FAIL')
        self.assertEqual(result['failure_code'], 'PROVISION_FAILED')
        self.assertEqual(result['cleanup'], 'CONFIRMED')
        self.assertEqual(self.executor.invocations, 0)
        self.assertFalse((self.s.folder / 'catalog-proof.json').exists())
        self.assertNotIn('private-catalog-content', json.dumps(result))

    def test_host_authentication_trust_md5_errors_includes_and_drift_refused(self):
        mutations = [
            lambda v: v['rules'][1].update(auth_method='trust'),
            lambda v: v['rules'][1].update(auth_method='md5'),
            lambda v: v['rules'][1].update(auth_method='password'),
            lambda v: v['rules'][1].update(error='unreviewed parse error'),
            lambda v: v['rules'][1].update(file_name='/foreign/hba.conf'),
            lambda v: v['rules'][1].update(type='include'),
            lambda v: v['rules'][1].update(options=['map=unreviewed']),
            lambda v: v.update(hba_file='/foreign/hba.conf'),
            lambda v: v.update(file_is_directory=True),
            lambda v: v.update(file_mtime_epoch_us=v['config_load_epoch_us'] + 1),
            lambda v: v.update(rule_count=33),
            lambda v: v.update(session_user='different_admin'),
            lambda v: v['rules'][-1].update(address='127.0.0.1', netmask='255.255.255.255'),
        ]
        for change in mutations:
            with self.subTest(change=mutations.index(change)):
                value = fake_hba('wt_admin_' + 'a' * 32)
                change(value)
                with self.assertRaises(m.Fault):
                    m.verify_hba_snapshot(value, 'wt_admin_' + 'a' * 32)
        self.executor.hba_mutation = mutations[0]
        result = self.run_fake()
        self.assertEqual(result['failure_code'], 'PG_AUTHENTICATION_MISMATCH')
        self.assertEqual(self.executor.invocations, 0)
        self.assertFalse(any(b'CREATE ROLE' in data for _, data, *_ in self.executor.calls))
        self.assertEqual(result['cleanup'], 'CONFIRMED')

    def test_wrong_password_success_and_non_auth_failure_refused(self):
        for response in (m.Result(0, b'1\n'), m.Result(2, b'', b'connection refused'),
                         m.Result(2, b'', b'password authentication failed for a different user')):
            case = SupervisorTests('runTest')
            case.setUp()
            try:
                case.executor.bad_password_result = response
                result = case.run_fake()
                self.assertEqual(result['failure_code'], 'PG_AUTHENTICATION_MISMATCH')
                self.assertEqual(result['authentication']['status'], 'NOT_VERIFIED')
                self.assertEqual(case.executor.invocations, 0)
                self.assertEqual(result['cleanup'], 'CONFIRMED')
            finally:
                case.tearDown()

    def test_authentication_probe_bracket_and_safe_retained_facts(self):
        result = self.run_fake()
        auth = result['authentication']
        self.assertEqual(auth['status'], 'VERIFIED_IN_ISOLATED_SCOPE')
        self.assertEqual(auth['wrong_password_probe'], 'AUTH_DENIAL_OBSERVED')
        self.assertEqual(auth['successful_admin_identity_reads'], 2)
        self.assertEqual(auth['loaded_hba_cache'], 'NOT_DIRECTLY_EXPOSED_BY_POSTGRESQL')
        hba = [(args, data) for args, data, *_ in self.executor.calls if b'pg_hba_file_rules' in data]
        self.assertEqual(len(hba), 2)
        self.assertTrue(all('psql -w' in ' '.join(args) for args, _ in hba))
        self.assertTrue(all('LC_ALL=C' in ' '.join(args) for args, _ in hba))
        bad = [(args, data) for args, data, *_ in self.executor.calls if data.endswith(b'SELECT 1;\n')]
        self.assertEqual(len(bad), 1)
        self.assertNotEqual(bad[0][1].splitlines()[0], hba[0][1].splitlines()[0])
        self.assertNotIn('password authentication failed', json.dumps(result))
        self.assertNotIn('28P01', json.dumps(result))

    def test_hba_load_time_need_not_follow_postmaster_start(self):
        value = fake_hba('wt_admin_' + 'a' * 32)
        value['config_load_epoch_us'] = value['postmaster_start_epoch_us'] - 1
        fixed, rules = m.verify_hba_snapshot(value, 'wt_admin_' + 'a' * 32)
        self.assertEqual(fixed['config_load_epoch_us'], value['config_load_epoch_us'])
        self.assertTrue(rules)

    def test_hba_file_symlink_outside_owned_data_is_refused(self):
        self.executor.hba_canonical_path = '/foreign/pg_hba.conf'
        result = self.run_fake()
        self.assertEqual(result['failure_code'], 'PG_AUTHENTICATION_MISMATCH')
        self.assertEqual(result['authentication']['wrong_password_probe'], 'NOT_RUN')
        self.assertEqual(self.executor.invocations, 0)
        self.assertEqual(result['cleanup'], 'CONFIRMED')

    def test_hba_snapshot_change_between_identity_reads_refused(self):
        self.executor.hba_after_mutation = lambda v: v.update(config_load_epoch_us=v['config_load_epoch_us'] + 1)
        result = self.run_fake()
        self.assertEqual(result['failure_code'], 'PG_AUTHENTICATION_MISMATCH')
        self.assertEqual(result['authentication']['wrong_password_probe'], 'AUTH_DENIAL_OBSERVED')
        self.assertEqual(result['authentication']['status'], 'NOT_VERIFIED')
        self.assertEqual(self.executor.invocations, 0)
        self.assertEqual(result['cleanup'], 'CONFIRMED')

    def test_bad_event_stream_never_becomes_pass(self):
        self.executor.output = b'PASS-looking-secret-that-must-not-be-printed'
        result = self.run_fake()
        self.assertEqual(result['status'], 'FAIL')
        self.assertEqual(result['cleanup'], 'CONFIRMED')
        self.assertNotIn('PASS-looking', json.dumps(result))
        self.assertEqual(self.executor.invocations, 1)

    def test_signal_failure_is_not_success_and_cleanup_runs(self):
        original = self.s.run_fixture
        def interrupted():
            self.s.interrupted = True
            raise m.Fault('INTERRUPTED', True)
        self.s.run_fixture = interrupted
        result = self.run_fake()
        self.assertEqual(result['failure_code'], 'INTERRUPTED')
        self.assertEqual(result['status'], 'FAIL')
        self.assertEqual(result['cleanup'], 'CONFIRMED')
        self.assertEqual(self.executor.removals, 1)

    def test_expired_or_expanded_job_deadline_never_launches(self):
        for deadline in (time.monotonic_ns() - 1, time.monotonic_ns() + 900_000_000_000):
            self.config.job_deadline_ns = deadline
            result = self.run_fake()
            self.assertEqual(result['status'], 'FAIL')
            self.assertEqual(self.executor.calls, [])

    def test_insufficient_create_budget_does_not_send_create(self):
        self.config.job_deadline_ns = time.monotonic_ns() + 140_000_000_000
        result = self.run_fake()
        self.assertEqual(result['failure_code'], 'SUBPROCESS_TIMEOUT')
        self.assertFalse(any(args[0] == m.DOCKER and args[1] == 'create' for args, *_ in self.executor.calls))
        self.assertEqual(result['cleanup'], 'CONFIRMED')

    def test_cleanup_deadline_cannot_be_extended_by_new_config(self):
        self.assertEqual(self.run_fake()['status'], 'PASS_FOCUSED_ISOLATED_SCOPE')
        self.config.job_deadline_ns += 1_000_000_000
        before = len(self.executor.calls)
        with self.assertRaises(m.Fault):
            self.s.cleanup()
        self.assertEqual(len(self.executor.calls), before)

    def test_failed_settings_are_not_reported_verified(self):
        self.executor.fail = 'settings'
        result = self.run_fake()
        self.assertEqual(result['resource_policy']['container_inspection'], 'VERIFIED')
        self.assertEqual(result['resource_policy']['server_settings'], 'NOT_VERIFIED')
        self.assertEqual(result['status'], 'FAIL')

    def test_admission_posture_settings_fail_closed_and_cleanup(self):
        changes = {'shared_preload': 'unexpected_worker', 'session_preload': 'unexpected_worker',
            'local_preload': 'unexpected_worker', 'archive_mode': 'on', 'summarize_wal': 'on',
            'io_method': 'sync', 'io_workers': '4'}
        for key, value in changes.items():
            with self.subTest(key=key):
                case = SupervisorTests('runTest')
                case.setUp()
                try:
                    case.executor.settings_mutation = lambda settings, k=key, v=value: settings.update({k: v})
                    result = case.run_fake()
                    self.assertEqual(result['status'], 'FAIL')
                    self.assertEqual(result['failure_code'], 'PG_SETTINGS_MISMATCH')
                    self.assertEqual(result['resource_policy']['server_settings'], 'NOT_VERIFIED')
                    self.assertEqual(case.executor.invocations, 0)
                    self.assertEqual(case.executor.removals, 1)
                    self.assertEqual(result['cleanup'], 'CONFIRMED')
                finally:
                    case.tearDown()

    def test_admission_boolean_output_is_exact_and_failure_never_zero(self):
        for output in (b'[{"activity_admitted":false}]', b'[{"activity_admitted":0}]',
                b'[{"activity_admitted":1}]', b'[{"activity_admitted":null}]', b'[]',
                b'[{"activity_admitted":true,"extra":0}]', b'[{},{}]', b't\n', b''):
            with self.subTest(output=output):
                case = SupervisorTests('runTest')
                case.setUp()
                try:
                    case.executor.activity_output = output
                    result = case.run_fake()
                    self.assertEqual(result['status'], 'FAIL')
                    self.assertEqual(case.executor.invocations, 0)
                    self.assertEqual(case.executor.removals, 1)
                    self.assertEqual(result['cleanup'], 'CONFIRMED')
                finally:
                    case.tearDown()

    def test_wrong_image_never_marks_container_verified(self):
        self.executor.inspect_mutation = {'image_id': 'sha256:' + 'e' * 64}
        result = self.run_fake()
        self.assertEqual(result['resource_policy']['container_inspection'], 'NOT_VERIFIED')
        self.assertEqual(result['resource_policy']['server_settings'], 'NOT_VERIFIED')
        self.assertNotIn('container_observation', result)

    def test_capability_singleton_equivalence_only(self):
        for spelling in ('NET_RAW', 'CAP_NET_RAW'):
            with self.subTest(spelling=spelling):
                case = SupervisorTests('runTest')
                case.setUp()
                try:
                    case.executor.inspect_mutation = {'cap_drop': [spelling]}
                    result = case.run_fake()
                    self.assertEqual(result['status'], 'PASS_FOCUSED_ISOLATED_SCOPE')
                    checks = result['resource_diagnostics']['post_start']['checks']
                    self.assertEqual(checks['drop_net_raw_only'], 'MATCH')
                    self.assertEqual(checks['drop_net_raw_literal'], 'MATCH' if spelling == 'NET_RAW' else 'MISMATCH')
                    self.assertEqual(checks['drop_net_raw_canonical'], 'MATCH' if spelling == 'CAP_NET_RAW' else 'MISMATCH')
                    create = next(args for args, *_ in case.executor.calls if args[:2] == [m.DOCKER, 'create'])
                    self.assertEqual(create[create.index('--cap-drop') + 1], 'NET_RAW')
                    self.assertEqual(case.executor.invocations, 1)
                finally:
                    case.tearDown()

    def test_capability_missing_duplicate_extra_other_rejected(self):
        for value in (None, [], ['NET_RAW', 'NET_RAW'], ['CAP_NET_RAW', 'CAP_NET_RAW'],
                      ['NET_RAW', 'CAP_NET_RAW'], ['CAP_NET_RAW', 'SYS_ADMIN'], ['ALL'],
                      ['NET_ADMIN'], ['cap_net_raw'], [' CAP_NET_RAW '], 'CAP_NET_RAW', [None]):
            with self.subTest(value=value):
                case = SupervisorTests('runTest')
                case.setUp()
                try:
                    case.executor.inspect_mutation = {'cap_drop': value}
                    result = case.run_fake()
                    self.assertEqual(result['failure_code'], 'RESOURCE_MISMATCH')
                    self.assertEqual(result['resource_diagnostics']['post_start']['checks']['drop_net_raw_only'], 'MISMATCH')
                    self.assertEqual(result['cleanup'], 'CONFIRMED')
                    self.assertEqual(case.executor.invocations, 0)
                    self.assertFalse(any(args[:2] == [m.DOCKER, 'exec'] for args, *_ in case.executor.calls))
                finally:
                    case.tearDown()

    def test_resource_diagnostics_capture_all_fixed_checks_before_rejection(self):
        self.executor.inspect_mutation = {'memory': 2 << 30, 'cpus': 3_000_000_000,
            'cap_drop': ['CAP_NET_RAW'], 'image_id': 'sha256:' + 'e' * 64,
            'ports': {'5432/tcp': [{'HostIp': '0.0.0.0', 'HostPort': '54321'}]}}
        result = self.run_fake()
        self.assertEqual(result['failure_code'], 'RESOURCE_MISMATCH')
        self.assertEqual(result['resource_policy']['container_inspection'], 'NOT_VERIFIED')
        self.assertEqual(set(result['resource_diagnostics']), {'post_start'})
        checks = result['resource_diagnostics']['post_start']['checks']
        for key in ('memory_limit', 'cpu_limit', 'image_id_matches', 'port_loopback'):
            self.assertEqual(checks[key], 'MISMATCH')
        for key in ('owned_identity', 'drop_net_raw_canonical', 'drop_net_raw_only', 'running', 'memory_swap_limit'):
            self.assertEqual(checks[key], 'MATCH')
        self.assertEqual(checks['port_unchanged'], 'NOT_APPLICABLE')
        retained = json.loads((self.s.folder / 'outcome.json').read_bytes())
        self.assertEqual(retained['resource_diagnostics'], result['resource_diagnostics'])

    def test_diagnostics_preserve_null_and_lifecycle_rejection_decisions(self):
        for change, code, key, category in (
            ({'running': False}, 'RESOURCE_MISMATCH', 'running', 'MISMATCH'),
            ({'ports': None}, 'INTERNAL_FAILURE', 'port_keys', 'INVALID_SHAPE'),
            ({'ports': {}}, 'RESOURCE_MISMATCH', 'port_keys', 'MISMATCH'),
            ({'ports': {'5432/tcp': None}}, 'RESOURCE_MISMATCH', 'port_single_binding', 'MISMATCH'),
            ({'ports': {'5432/tcp': []}}, 'RESOURCE_MISMATCH', 'port_single_binding', 'MISMATCH'),
            ({'mounts': None}, 'RESOURCE_MISMATCH', 'mounts_tmpfs_only', 'MISMATCH')):
            with self.subTest(change=change):
                case = SupervisorTests('runTest')
                case.setUp()
                try:
                    case.executor.inspect_mutation = change
                    result = case.run_fake()
                    self.assertEqual(result['failure_code'], code)
                    self.assertEqual(result['resource_diagnostics']['post_start']['checks'][key], category)
                    if change == {'ports': {'5432/tcp': []}}:
                        for empty_key in ('port_loopback', 'port_range'):
                            self.assertEqual(result['resource_diagnostics']['post_start']['checks'][empty_key], 'INVALID_SHAPE')
                    self.assertEqual(result['cleanup'], 'CONFIRMED')
                    self.assertEqual(case.executor.invocations, 0)
                finally:
                    case.tearDown()

    def test_resource_diagnostics_are_fixed_categories_without_observed_values(self):
        sentinel = 'NEVER_RETAIN_PRIVATE_DIAGNOSTIC_VALUE'
        self.executor.inspect_mutation = {'cap_drop': [sentinel], 'cap_add': [sentinel],
            'tmpfs': {sentinel: sentinel}, 'mounts': [{'Type': 'bind', 'Destination': sentinel, 'Source': sentinel}],
            'ports': {sentinel: [{'HostIp': sentinel, 'HostPort': sentinel}]}, 'env': [sentinel],
            'raw_error': sentinel, 'other': {'secret': sentinel}}
        result = self.run_fake()
        rendered = json.dumps(result)
        self.assertNotIn(sentinel, rendered)
        self.assertNotIn(self.s.marker['owner'], rendered)
        self.assertNotIn(self.s.identity.key, rendered)
        self.assertNotIn('container_observation', result)
        record = result['resource_diagnostics']['post_start']
        self.assertEqual(set(record), {'format', 'authority', 'checks'})
        self.assertEqual(record['authority'], 'DIAGNOSTIC_ONLY')
        self.assertEqual(set(record['checks']), {
            'owned_identity', 'image_id_matches', 'memory_limit', 'memory_swap_limit', 'cpu_limit', 'shm_limit',
            'unprivileged', 'bridge_network', 'private_pid', 'private_ipc', 'no_added_capabilities',
            'drop_net_raw_literal', 'drop_net_raw_canonical', 'drop_net_raw_only', 'running', 'tmpfs_exact',
            'mounts_tmpfs_only', 'port_keys', 'port_single_binding', 'port_loopback', 'port_range', 'port_unchanged'})
        self.assertLess(len(m.canonical(record)), 2048)
        self.assertLessEqual(set(record['checks'].values()), {'MATCH', 'MISMATCH', 'INVALID_SHAPE', 'NOT_APPLICABLE'})

    def test_diagnostics_add_no_docker_operations(self):
        result = self.run_fake()
        self.assertEqual(set(result['resource_diagnostics']), {'post_start', 'pre_fixture'})
        self.assertEqual(result['resource_diagnostics']['pre_fixture']['checks']['port_unchanged'], 'MATCH')
        actions = [args[1] for args, *_ in self.executor.calls if args[0] == m.DOCKER]
        self.assertEqual(actions.count('inspect'), 3)  # post-start, pre-fixture, owned cleanup
        for action in ('pull', 'image', 'create', 'start', 'rm'):
            self.assertEqual(actions.count(action), 1)
        before = len(self.executor.calls)
        with patch.object(m.subprocess, 'Popen', side_effect=AssertionError('MUST_NOT_LAUNCH')):
            for view in (None, {}, [], 0, 'untrusted', {'ports': {'5432/tcp': []}}, {'id': None}):
                record = m.container_diagnostics(view, self.s.identity, 'b' * 64, 'c' * 64, 'sha256:' + 'd' * 64)
                self.assertLessEqual(set(record['checks'].values()), {'MATCH', 'MISMATCH', 'INVALID_SHAPE', 'NOT_APPLICABLE'})
        self.assertEqual(len(self.executor.calls), before)

    def test_pre_fixture_diagnostics_do_not_overwrite_post_start(self):
        original = self.executor.call
        def mutate_pre_fixture(args, **kwargs):
            result = original(args, **kwargs)
            if args[:2] == [m.DOCKER, 'inspect'] and self.s.stage == 'fixture':
                view = json.loads(result.stdout)
                view['ports']['5432/tcp'][0]['HostPort'] = '54322'
                return m.Result(0, m.canonical(view))
            return result
        self.executor.call = mutate_pre_fixture
        result = self.run_fake()
        self.assertEqual(result['failure_code'], 'RESOURCE_MISMATCH')
        self.assertEqual(result['failure_stage'], 'fixture')
        self.assertEqual(result['resource_diagnostics']['post_start']['checks']['port_unchanged'], 'NOT_APPLICABLE')
        self.assertEqual(result['resource_diagnostics']['pre_fixture']['checks']['port_unchanged'], 'MISMATCH')
        self.assertEqual(result['cleanup'], 'CONFIRMED')
        self.assertEqual(self.executor.invocations, 0)

    def test_execute_diagnostics_preserve_process_json_shape_and_output_failures(self):
        step = types.SimpleNamespace(phase='bootstrap_preflight', login_role='unused', database='unused',
                                     sql='OFFLINE_NO_EXECUTION', timeout_seconds=30, query=True)
        cases = [(m.Fault('SUBPROCESS_NONZERO'), 'SQL_PROCESS_NONZERO', 'SUBPROCESS_NONZERO'),
                 (m.Fault('SUBPROCESS_TIMEOUT', True), 'SQL_PROCESS_TIMEOUT', 'SUBPROCESS_TIMEOUT'),
                 (m.Fault('OUTPUT_LIMIT'), 'SQL_PROCESS_OUTPUT_LIMIT', 'OUTPUT_LIMIT'),
                 (m.Fault('INTERRUPTED'), 'INTERRUPTED', 'INTERRUPTED'),
                 (m.Fault('SUBPROCESS_FAILED'), 'SQL_PROCESS_FAILED', 'SUBPROCESS_FAILED'),
                 (b'PRIVATE_NON_JSON', 'JSON_DECODE_FAILED', 'INVALID_OUTPUT'),
                 (b'{}', 'JSON_SHAPE_INVALID', 'INVALID_OUTPUT'),
                 (b'[null]', 'JSON_SHAPE_INVALID', 'INVALID_OUTPUT'),
                 (m.canonical([{}] * 4097), 'JSON_SHAPE_INVALID', 'INVALID_OUTPUT')]
        for result, classification, code in cases:
            with self.subTest(classification=classification):
                with patch.object(self.s, 'sql_call', side_effect=result if isinstance(result, BaseException) else None,
                                  return_value=result):
                    with self.assertRaises(m.Fault) as caught:
                        self.s.execute(step)
                self.assertEqual(caught.exception.code, code)
                self.assertEqual(self.s.provision_diagnostics, {'bootstrap_preflight': classification})
                self.assertEqual(self.s.stage, 'provision_bootstrap_preflight')
        step.phase, step.query = 'owner_schema', False
        with patch.object(self.s, 'sql_call', return_value=b'PRIVATE_UNEXPECTED_OUTPUT'):
            with self.assertRaises(m.Fault) as caught:
                self.s.execute(step)
        self.assertEqual(caught.exception.code, 'INVALID_OUTPUT')
        self.assertEqual(self.s.provision_diagnostics['owner_schema'], 'UNEXPECTED_NONQUERY_OUTPUT')
        self.assertNotIn('PRIVATE', json.dumps(self.s.provision_diagnostics))
        self.assertEqual(self.executor.calls, [])

    def test_preflight_diagnostic_sink_rejects_nonallowlisted_content_without_effect(self):
        valid = {'format': 'worktool-registration-preflight-fields-v1', 'authority': 'DIAGNOSTIC_ONLY',
            'classification': 'VALUE_MISMATCH', 'shape': {key: 'MATCH' for key in ('root', 'row_count', 'row', 'fields')},
            'checks': {key: 'MATCH' for key in m.PREFLIGHT_CHECKS}}
        valid['checks']['activity_admitted'] = 'MISMATCH'
        mutations = [None, [], 'PRIVATE_VALUE', {}, valid | {'raw': 'PRIVATE_VALUE'},
            valid | {'classification': 'PRIVATE_VALUE'}, valid | {'checks': valid['checks'] | {'session_user': 'PRIVATE_VALUE'}},
            valid | {'shape': valid['shape'] | {'row_count': 1}}, valid | {'authority': 'ACCEPTANCE'},
            valid | {'checks': {'version': 'MATCH'}}, valid | {'checks': []}, valid | {'shape': None}]
        for diagnostic in mutations:
            self.s.record_preflight_diagnostics(diagnostic)
            self.assertIsNone(self.s.preflight_diagnostics)
        self.s.record_preflight_diagnostics(valid)
        snapshot = copy.deepcopy(self.s.preflight_diagnostics)
        valid['checks']['activity_admitted'] = 'PRIVATE_MUTATION_AFTER_CALLBACK'
        self.assertEqual(self.s.preflight_diagnostics, snapshot)
        self.assertLess(len(m.canonical(snapshot)), 1024)
        self.assertEqual(self.executor.calls, [])
        self.s.record_execution_diagnostic('PRIVATE_ROLE_OR_PHASE', 'SQL_PROCESS_FAILED')
        self.s.record_execution_diagnostic('bootstrap_preflight', 'PRIVATE_ERROR')
        self.assertEqual(self.s.provision_diagnostics, {})

    def test_real_helper_preflight_failures_retain_exact_redacted_class_and_cleanup(self):
        actual_root = path.resolve().parents[1]
        provision = m.load_helper('pg-registration-provision.py')
        for variant, classification, field_class in (
                ('process', 'SQL_PROCESS_NONZERO', None), ('json', 'JSON_DECODE_FAILED', None),
                ('shape', 'JSON_SHAPE_INVALID', None), ('fields', 'QUERY_RESULT_RETURNED', 'INVALID_SHAPE'),
                ('pristine', 'QUERY_RESULT_RETURNED', 'VALUE_MISMATCH')):
            with self.subTest(variant=variant):
                case = SupervisorTests('runTest')
                case.setUp()
                try:
                    class PreflightExecutor(FakeExecutor):
                        def phase(self, args):
                            if args[0] == m.DOCKER and args[1] == 'exec' and '-i' in args and self.supervisor.stage.startswith('provision_'):
                                return 'sql_' + self.supervisor.stage.removeprefix('provision_')
                            return super().phase(args)
                        def call(self, args, **kwargs):
                            result = super().call(args, **kwargs)
                            if self.phase(args) != 'sql_bootstrap_preflight':
                                return result
                            row = dict(version=180006, session_user=self.supervisor.admin, current_user=self.supervisor.admin,
                                database_absent=True, roles_absent=True, activity_admitted=True, bootstrap_oid=10, bootstrap_superuser=True, event_triggers=0)
                            if variant == 'process':
                                return m.Result(2, b'', b'PRIVATE_SQL_ERROR_WITH_CREDENTIALS')
                            if variant == 'json':
                                return m.Result(0, b'PRIVATE_UNPARSEABLE_RESULT')
                            if variant == 'shape':
                                return m.Result(0, m.canonical(row))
                            if variant == 'fields':
                                row['PRIVATE_EXTRA_FIELD'] = 'PRIVATE_VALUE'
                            else:
                                row['activity_admitted'] = False
                            return m.Result(0, m.canonical([row]))
                    source_bytes = m.make_pin(actual_root)
                    case.source_pin.write_bytes(source_bytes)
                    case.config.source_root, case.config.source_pin_sha256 = actual_root, m.sha(source_bytes)
                    executor = PreflightExecutor()
                    supervisor = m.Supervisor(case.config, executor, provision, FakeEvidence(), sleep=lambda _: None)
                    executor.supervisor = supervisor
                    with patch.object(m.subprocess, 'Popen', side_effect=AssertionError('MUST_NOT_LAUNCH')):
                        result = supervisor.run(handle_signals=False)
                    self.assertEqual(result['failure_code'], 'PROVISION_FAILED')
                    self.assertEqual(result['failure_stage'], 'provision_bootstrap_preflight')
                    self.assertEqual(result['provision_diagnostics']['phases'], {'bootstrap_preflight': classification})
                    if field_class is None:
                        self.assertNotIn('preflight_diagnostics', result)
                    else:
                        self.assertEqual(result['preflight_diagnostics']['classification'], field_class)
                        if variant == 'pristine':
                            self.assertEqual(result['preflight_diagnostics']['checks']['activity_admitted'], 'MISMATCH')
                    self.assertEqual(result['cleanup'], 'CONFIRMED')
                    self.assertEqual(executor.invocations, 0)
                    self.assertEqual(executor.removals, 1)
                    sql_calls = [call for call in executor.calls if call[0][:3] == [m.DOCKER, 'exec', '-i'] and b'json_build_array' in call[1]]
                    self.assertEqual(len(sql_calls), 1)
                    self.assertFalse(any(b'CREATE ROLE' in call[1] for call in executor.calls))
                    retained = json.loads((supervisor.folder / 'outcome.json').read_bytes())
                    self.assertEqual(retained['provision_diagnostics'], result['provision_diagnostics'])
                    self.assertNotIn('PRIVATE_', json.dumps(result))
                    self.assertNotIn(supervisor.admin, json.dumps(result.get('preflight_diagnostics')))
                finally:
                    case.tearDown()

    def test_popen_failure_is_redacted(self):
        with patch.object(m.subprocess, 'Popen', side_effect=OSError('secret-should-not-escape')):
            with self.assertRaises(m.Fault) as caught:
                m.Executor().call(['/fake/never-executed'])
        self.assertEqual(str(caught.exception), 'SUBPROCESS_FAILED')
        self.assertNotIn('secret', str(caught.exception))

    def test_executor_timeout_kills_fake_process_group(self):
        class Stream:
            closed = False
            def __init__(self, fd): self.fd = fd
            def fileno(self): return self.fd
            def close(self): self.closed = True
        class Select:
            def __init__(self): self.entries = {}
            def register(self, stream, event, data): self.entries[stream.fd] = stream
            def get_map(self): return self.entries
            def close(self): pass
        process = types.SimpleNamespace(stdin=Stream(100), stdout=Stream(101), stderr=Stream(102), pid=12345,
                                        wait=lambda timeout: 0)
        with patch.object(m.subprocess, 'Popen', return_value=process), patch.object(m.os, 'set_blocking'), \
             patch.object(m.selectors, 'DefaultSelector', Select), patch.object(m.time, 'monotonic', side_effect=[0, 2]), \
             patch.object(m.os, 'killpg') as kill:
            with self.assertRaises(m.Fault) as caught:
                m.Executor().call(['/fake/never-executed'], timeout=1)
        self.assertEqual(caught.exception.code, 'SUBPROCESS_TIMEOUT')
        self.assertTrue(caught.exception.uncertain)
        kill.assert_called_once_with(12345, signal.SIGKILL)
        self.assertTrue(process.stdin.closed and process.stdout.closed and process.stderr.closed)

    def test_only_toolchain_can_allow_trusted_root_owned_directory(self):
        original = m.Path.lstat
        owner, mode = [0], [0o755]
        target = self.go_root
        def fake_lstat(path):
            info = original(path)
            if path == target:
                values = list(info)
                values[0] = (info.st_mode & ~0o777) | mode[0]
                values[4] = owner[0]
                return os.stat_result(values)
            return info
        with patch.object(m.Path, 'lstat', fake_lstat), patch.object(m.os, 'getuid', return_value=1000):
            self.assertEqual(m.checked_directory(target, allow_root_owner=True), target)
            with self.assertRaises(m.Fault):
                m.checked_directory(target)
            owner[0] = 2000
            with self.assertRaises(m.Fault):
                m.checked_directory(target, allow_root_owner=True)
            owner[0], mode[0] = 0, 0o775
            with self.assertRaises(m.Fault):
                m.checked_directory(target, allow_root_owner=True)

    def test_executing_module_must_match_pinned_source_root(self):
        result = self.s.run(handle_signals=False)
        self.assertEqual(result['failure_code'], 'SOURCE_PIN_MISMATCH')
        self.assertEqual(self.executor.calls, [])

    def test_absolute_deadline_refuses_more_work_but_reserves_cleanup(self):
        clock = [100.0]
        self.s.monotonic = lambda: clock[0]
        self.s.operation_deadline = 110.0
        self.s.call([m.DOCKER, 'ps', '-aq'], timeout=30)
        self.assertEqual(self.executor.calls[-1][-1], 10.0)
        clock[0] = 111.0
        before = len(self.executor.calls)
        with self.assertRaises(m.Fault):
            self.s.call([m.DOCKER, 'ps', '-aq'])
        self.assertEqual(len(self.executor.calls), before)

    def test_catalog_row_bound_accepts_reviewed_size_and_rejects_excess(self):
        self.s.credentials = {m.ROLE_NAMES[0]: 'a' * 64}
        self.s.database = 'wt_test_12345678'
        step = types.SimpleNamespace(phase='catalog', login_role=m.ROLE_NAMES[0], database=self.s.database,
                                     sql='SELECT inert;', query=True, timeout_seconds=30)
        self.s.sql_call = lambda *a, **kw: m.canonical([{'kind': 'catalog'}] * 2048)
        self.assertEqual(len(self.s.execute(step)), 2048)
        self.s.sql_call = lambda *a, **kw: m.canonical([{'kind': 'catalog'}] * 4097)
        with self.assertRaises(m.Fault):
            self.s.execute(step)
        self.assertEqual(self.executor.calls, [])

    def test_unbounded_executor_inputs_refused_without_popen(self):
        with patch.object(m.subprocess, 'Popen', side_effect=AssertionError('MUST_NOT_LAUNCH')):
            for args, data, timeout in [([], b'', 1), (['x'], b'', 301), (['x'], b'x' * ((2 << 20) + 1), 1),
                                       (['x\x00bad'], b'', 1)]:
                with self.assertRaises(m.Fault):
                    m.Executor().call(args, data=data, timeout=timeout)

    def test_cleanup_does_not_adopt_unmarked_directory(self):
        self.s.folder.mkdir(mode=0o700)
        m.private_file(self.s.folder / 'foreign', b'KEEP')
        with self.assertRaises((m.Fault, OSError)):
            self.s.cleanup()
        self.assertEqual((self.s.folder / 'foreign').read_bytes(), b'KEEP')
        self.assertEqual(self.executor.calls, [])

    def test_symlink_and_duplicate_pin_refused(self):
        (self.source / 'redirect').symlink_to(self.root / 'source-pin.json')
        with self.assertRaises(m.Fault):
            m.make_pin(self.source)
        self.assertEqual(self.executor.calls, [])


def census_wire():
    categories = ('NOT_INITIALIZED', 'ARCHIVER', 'AUTOVACUUM_LAUNCHER', 'AUTOVACUUM_WORKER', 'CLIENT_BACKEND',
        'DEAD_END_CLIENT_BACKEND', 'BACKGROUND_WORKER', 'BACKGROUND_WRITER', 'CHECKPOINTER', 'IO_WORKER', 'LOGGER',
        'SLOTSYNC_WORKER', 'STANDALONE_BACKEND', 'STARTUP', 'WALRECEIVER', 'WALSENDER', 'WALSUMMARIZER', 'WALWRITER',
        'LOGICAL_REPLICATION_LAUNCHER', 'TYPE_NULL', 'TYPE_OTHER')
    bins = []
    for category in categories:
        for mask in range(16):
            flags = [bool(mask & bit) for bit in (8, 4, 2, 1)]
            bins.append(dict(backend_category=category, role_oid_present=flags[0], role_name_present=flags[1],
                database_oid_present=flags[2], database_name_present=flags[3],
                counted_by_original_predicate=category in ('CLIENT_BACKEND', 'TYPE_NULL') or flags[1], count=0))
    return {'format': 'worktool-registration-activity-census-wire-v1', 'status': 'OBSERVED', 'observation': {
        'scope': 'PG_STAT_ACTIVITY_CLUSTER_EXCLUDING_SELF', 'rows_total': 0, 'counted_total': 0, 'not_counted_total': 0, 'bins': bins}}


class CensusTests(unittest.TestCase):
    def setUp(self):
        self.base = SupervisorTests('runTest')
        self.base.setUp()
        helper = m.load_helper('pg-registration-provision.py')
        class Provision(FakeProvision):
            activity_census_query = staticmethod(helper.activity_census_query)
            validate_activity_census_wire = staticmethod(helper.validate_activity_census_wire)
            def provision(self, executor, bundle, database, bootstrap, passwords):
                rows = executor.execute(types.SimpleNamespace(phase='bootstrap_preflight', login_role=bootstrap,
                    database='postgres', sql='SELECT census_test_pristine;', timeout_seconds=30, query=True))
                expected = [dict(version=180006, session_user=bootstrap, current_user=bootstrap, database_absent=True,
                    roles_absent=True, activity_admitted=True, bootstrap_oid=10, bootstrap_superuser=True, event_triggers=0)]
                executor.record_preflight_diagnostics(helper.preflight_field_diagnostics(rows, expected))
                raise helper.ProvisionError('BOOTSTRAP_NOT_PRISTINE')
        class Executor(FakeExecutor):
            census_result = None
            def phase(self, args):
                if args[:3] == [m.DOCKER, 'exec', '-i']:
                    if b'worktool-registration-activity-census-wire-v1' in self.last_data:
                        return 'census'
                    if b'census_test_pristine' in self.last_data:
                        return 'sql_bootstrap_preflight'
                return super().phase(args)
            def call(self, args, **kwargs):
                result = super().call(args, **kwargs)
                phase = self.phase(args)
                if phase == 'sql_bootstrap_preflight':
                    admin = self.supervisor.admin
                    return m.Result(0, m.canonical([dict(version=180006, session_user=admin, current_user=admin,
                        database_absent=True, roles_absent=True, activity_admitted=False, bootstrap_oid=10, bootstrap_superuser=True, event_triggers=0)]))
                if phase == 'census':
                    if isinstance(self.census_result, BaseException):
                        raise self.census_result
                    return self.census_result if self.census_result is not None else m.Result(0, m.canonical(census_wire()))
                return result
        self.helper = helper
        self.executor = Executor()
        self.s = m.Supervisor(self.base.config, self.executor, Provision(), FakeEvidence(), sleep=lambda _: None)
        self.executor.supervisor = self.s

    def tearDown(self):
        self.base.tearDown()

    def run_fake(self):
        with patch.object(m, '__file__', str(self.base.source / 'scripts/pg-registration-acceptance.py')), \
                patch.object(m.subprocess, 'Popen', side_effect=AssertionError('MUST_NOT_LAUNCH')):
            return self.s.run(handle_signals=False)

    def census_calls(self):
        return [call for call in self.executor.calls if b'worktool-registration-activity-census-wire-v1' in call[1]]

    def assert_failed_clean(self, result):
        self.assertEqual(result['status'], 'FAIL')
        self.assertEqual(result['failure_stage'], 'provision_bootstrap_preflight')
        self.assertEqual(result['cleanup'], 'CONFIRMED')
        self.assertEqual(self.executor.invocations, 0)
        self.assertEqual(self.executor.removals, 1)
        self.assertEqual(result['provision_diagnostics']['phases'], {'bootstrap_preflight': 'QUERY_RESULT_RETURNED'})
        self.assertEqual(result['activity_diagnostic_trigger'], {'failure_code': 'PROVISION_FAILED', 'failure_stage': 'provision_bootstrap_preflight'})
        self.assertFalse(any(b'CREATE ROLE' in call[1] for call in self.executor.calls))

    def test_one_later_empty_observation_never_changes_failure_or_cleanup(self):
        result = self.run_fake()
        self.assert_failed_clean(result)
        self.assertEqual(result['failure_code'], 'PROVISION_FAILED')
        record = result['activity_census']
        self.assertEqual(record['status'], 'OBSERVED')
        self.assertEqual(record['read_outcome'], 'PROCESS_ACKNOWLEDGED')
        self.assertEqual(record['original_rows_relation'], 'NOT_PROVEN')
        self.assertEqual(record['gate_disposition'], 'UNCHANGED_FAIL')
        self.assertEqual(record['observation']['rows_total'], 0)
        self.assertEqual(len(record['observation']['bins']), 336)
        self.assertEqual(len(self.census_calls()), 1)
        args, data, _, _, timeout = self.census_calls()[0]
        self.assertEqual(args[:3], [m.DOCKER, 'exec', '-i'])
        self.assertEqual(args[-2:], [self.s.admin, 'postgres'])
        self.assertEqual(data.split(b'\n', 1)[1].rstrip(), self.helper.activity_census_query().encode().rstrip())
        self.assertLessEqual(timeout, 5)
        before = len(self.executor.calls)
        self.s.collect_activity_census(result)
        self.assertEqual(len(self.executor.calls), before)
        self.assertEqual(self.s.cleanup(), 'CONFIRMED')
        self.assertEqual(len(self.census_calls()), 1)
        self.assertEqual(self.s.run(handle_signals=False)['failure_code'], 'RUN_ALREADY_STARTED')
        self.assertEqual(len(self.census_calls()), 1)

    def test_every_base_eligibility_condition_suppresses_the_read(self):
        mutations = [lambda s, o: o.update(failure_code='RUN_FAILED'), lambda s, o: o.update(failure_stage='fixture'),
            lambda s, o: setattr(s, 'stage', 'fixture'), lambda s, o: setattr(s, 'started', False),
            lambda s, o: s.provision_diagnostics.update(bootstrap_roles='EMPTY_RESULT_ACKNOWLEDGED'),
            lambda s, o: s.provision_diagnostics.update(bootstrap_preflight='JSON_SHAPE_INVALID'),
            lambda s, o: s.preflight_diagnostics.update(classification='VALUE_MATCH'),
            lambda s, o: s.preflight_diagnostics.update(authority='INVALID'),
            lambda s, o: setattr(s, 'container_verified', False), lambda s, o: setattr(s, 'server_settings_verified', False),
            lambda s, o: s.authentication_summary.update(status='NOT_VERIFIED'),
            lambda s, o: s.authentication_summary.update(wrong_password_probe='NOT_RUN'),
            lambda s, o: s.marker.update(run_invocations=1), lambda s, o: s.marker.update(run_invocations=False),
            lambda s, o: s.marker.update(state='FINISHED'), lambda s, o: s.marker.update(create_state='SENT'),
            lambda s, o: s.marker.update(owner='FOREIGN'), lambda s, o: s.marker.update(container_id='FOREIGN'),
            lambda s, o: s.marker.update(container_name='FOREIGN'), lambda s, o: s.marker.update(identity={}),
            lambda s, o: s.marker.update(source_sha256='FOREIGN'), lambda s, o: s.marker.update(job_deadline_ns=0),
            lambda s, o: s.container_observation.update(id='f' * 64),
            lambda s, o: s.container_observation.update(image='FOREIGN'),
            lambda s, o: s.container_observation.update(image_id='sha256:' + 'f' * 64),
            lambda s, o: s.credentials.clear(), lambda s, o: setattr(s, 'binding', {})]
        mutations += [lambda s, o, key=key: s.preflight_diagnostics['shape'].update({key: 'INVALID_SHAPE'})
                      for key in ('root', 'row_count', 'row', 'fields')]
        mutations += [lambda s, o, key=key: s.preflight_diagnostics['checks'].update({key: 'MATCH' if key == 'activity_admitted' else 'MISMATCH'})
                      for key in m.PREFLIGHT_CHECKS]
        original = self.s.collect_activity_census
        checked = []
        def inspect_eligibility(outcome):
            attrs = ('stage', 'started', 'provision_diagnostics', 'preflight_diagnostics', 'container_verified',
                'server_settings_verified', 'authentication_summary', 'marker', 'container_observation', 'credentials', 'binding')
            baseline = {key: copy.deepcopy(getattr(self.s, key)) for key in attrs}
            self.assertTrue(self.s.activity_census_eligible(outcome))
            for mutate in mutations:
                for key, value in baseline.items(): setattr(self.s, key, copy.deepcopy(value))
                trial = dict(outcome)
                mutate(self.s, trial)
                self.assertFalse(self.s.activity_census_eligible(trial))
                original(trial)
                self.assertIsNone(self.s.activity_census)
                checked.append(True)
            for key, value in baseline.items(): setattr(self.s, key, copy.deepcopy(value))
        self.s.collect_activity_census = inspect_eligibility
        result = self.run_fake()
        self.assert_failed_clean(result)
        self.assertEqual(len(checked), len(mutations))
        self.assertEqual(self.census_calls(), [])

    def test_budget_skip_and_immediate_pre_dispatch_recheck(self):
        for remaining, consume, expected_calls in ((10.999, 0, 0), (11.0, 0, 1), (11.0, .001, 0)):
            case = CensusTests('runTest'); case.setUp()
            try:
                original = case.s.collect_activity_census
                now = [100.0]
                def collect(outcome):
                    case.s.operation_deadline = now[0] + remaining
                    case.s.monotonic = lambda: now[0]
                    query = case.s.provision_helper.activity_census_query
                    def prepare():
                        now[0] += consume
                        return query()
                    case.s.provision_helper.activity_census_query = prepare
                    original(outcome)
                case.s.collect_activity_census = collect
                result = case.run_fake()
                case.assert_failed_clean(result)
                self.assertEqual(len(case.census_calls()), expected_calls)
                if not expected_calls:
                    self.assertEqual(result['activity_census']['status'], 'SKIPPED_BUDGET')
                    self.assertEqual(result['activity_census']['read_outcome'], 'NOT_ATTEMPTED')
            finally:
                case.tearDown()

    def test_acknowledged_validation_deadline_exhaustion_discards_observation(self):
        for point in ('before_validation', 'during_validation', 'after_reconstruction'):
            case = CensusTests('runTest'); case.setUp()
            try:
                clock = [100.0]
                original = case.s.collect_activity_census
                def collect(outcome):
                    case.s.operation_deadline = 111.0
                    case.s.monotonic = lambda: clock[0]
                    original(outcome)
                case.s.collect_activity_census = collect
                process = case.s.sql_process
                def sql(*args, **kwargs):
                    response = process(*args, **kwargs)
                    if 'worktool-registration-activity-census-wire-v1' in args[2] and point == 'before_validation':
                        clock[0] = 112.0
                    return response
                case.s.sql_process = sql
                validate = case.s.provision_helper.validate_activity_census_wire
                def validator(value):
                    result = validate(value)
                    if point == 'during_validation': clock[0] += 1.001
                    return result
                case.s.provision_helper.validate_activity_census_wire = validator
                canonical = m.canonical
                def encoded(value):
                    result = canonical(value)
                    if point == 'after_reconstruction' and isinstance(value, dict) and value.get('format') == 'worktool-registration-activity-census-v1' and value.get('status') == 'OBSERVED':
                        clock[0] += 1.001
                    return result
                with patch.object(m, 'canonical', side_effect=encoded):
                    result = case.run_fake()
                case.assert_failed_clean(result)
                self.assertEqual(result['activity_census']['status'], 'VALIDATION_BUDGET_EXHAUSTED')
                self.assertEqual(result['activity_census']['read_outcome'], 'PROCESS_ACKNOWLEDGED')
                self.assertIsNone(result['activity_census']['observation'])
                self.assertEqual(len(case.census_calls()), 1)
            finally:
                case.tearDown()

    def test_process_decode_shape_count_and_size_faults_never_leak_or_retry(self):
        bad_count = census_wire(); bad_count['observation']['rows_total'] = 4097
        overflow = {'format': 'worktool-registration-activity-census-wire-v1', 'status': 'COUNT_OUT_OF_RANGE', 'observation': None}
        cases = [(m.Fault('SUBPROCESS_TIMEOUT', True), 'SQL_PROCESS_TIMEOUT', 'UNCONFIRMED'),
            (m.Result(2, b'PRIVATE_OUTPUT', b'PRIVATE_ERROR'), 'SQL_PROCESS_NONZERO', 'UNCONFIRMED'),
            (m.Fault('OUTPUT_LIMIT'), 'SQL_PROCESS_OUTPUT_LIMIT', 'UNCONFIRMED'),
            (m.Fault('SUBPROCESS_FAILED'), 'SQL_PROCESS_FAILED', 'UNCONFIRMED'),
            (m.Result(0, b'PRIVATE_BAD_JSON'), 'JSON_DECODE_FAILED', 'PROCESS_ACKNOWLEDGED'),
            (m.Result(0, b'{"PRIVATE":1,"PRIVATE":2}'), 'JSON_DECODE_FAILED', 'PROCESS_ACKNOWLEDGED'),
            (m.Result(0, b'{"PRIVATE":NaN}'), 'JSON_DECODE_FAILED', 'PROCESS_ACKNOWLEDGED'),
            (m.Result(0, b'{"PRIVATE":1}'), 'JSON_SHAPE_INVALID', 'PROCESS_ACKNOWLEDGED'),
            (m.Result(0, b' ' * (m.CENSUS_MAX_BYTES + 1)), 'JSON_SIZE_LIMIT', 'PROCESS_ACKNOWLEDGED'),
            (m.Result(0, m.canonical(bad_count)), 'COUNT_OUT_OF_RANGE', 'PROCESS_ACKNOWLEDGED'),
            (m.Result(0, m.canonical(overflow)), 'COUNT_OUT_OF_RANGE', 'PROCESS_ACKNOWLEDGED')]
        for response, status, read in cases:
            case = CensusTests('runTest'); case.setUp()
            try:
                case.executor.census_result = response
                result = case.run_fake()
                case.assert_failed_clean(result)
                self.assertEqual(result['failure_code'], 'PROVISION_FAILED')
                self.assertEqual(result['activity_census']['status'], status)
                self.assertEqual(result['activity_census']['read_outcome'], read)
                self.assertIsNone(result['activity_census']['observation'])
                self.assertEqual(len(case.census_calls()), 1)
                self.assertNotIn('PRIVATE', json.dumps(result))
            finally:
                case.tearDown()

    def test_signal_windows_keep_trigger_final_interrupted_and_cleanup(self):
        for point in ('eligibility', 'pending', 'preparation', 'dispatch', 'validation'):
            case = CensusTests('runTest'); case.setUp()
            try:
                def interrupted(*args, **kwargs):
                    case.s.activity_diagnostic_trigger = None  # Exercise finally fallback after capture.
                    case.s.interrupted = True
                    raise m.Fault('INTERRUPTED', True)
                if point == 'eligibility': case.s.activity_census_eligible = interrupted
                elif point == 'pending':
                    original = case.s.collect_activity_census
                    def collect(outcome):
                        case.s.interrupted = True
                        original(outcome)
                    case.s.collect_activity_census = collect
                elif point == 'preparation': case.s.provision_helper.activity_census_query = interrupted
                elif point == 'validation': case.s.provision_helper.validate_activity_census_wire = interrupted
                else:
                    process = case.s.sql_process
                    def dispatch(*args, **kwargs):
                        if 'worktool-registration-activity-census-wire-v1' in args[2]:
                            self.assertTrue(case.s.census_attempted)
                            return interrupted()
                        return process(*args, **kwargs)
                    case.s.sql_process = dispatch
                result = case.run_fake()
                case.assert_failed_clean(result)
                self.assertEqual(result['failure_code'], 'INTERRUPTED')
                if point == 'eligibility': self.assertNotIn('activity_census', result)
                elif point == 'pending': self.assertEqual(result['activity_census']['status'], 'SKIPPED_INTERRUPTED')
                else: self.assertEqual(result['activity_census']['status'], 'INTERRUPTED')
            finally:
                case.tearDown()

    def test_deadline_expires_before_ack_without_dispatch_or_retry(self):
        clock = [100.0]
        original = self.s.collect_activity_census
        def collect(outcome):
            self.s.operation_deadline = 111.0
            self.s.monotonic = lambda: clock[0]
            original(outcome)
        self.s.collect_activity_census = collect
        process = self.s.sql_process
        def sql(*args, **kwargs):
            if 'worktool-registration-activity-census-wire-v1' in args[2]:
                self.assertTrue(self.s.census_attempted)
                self.assertEqual(self.s.operation_deadline, 111.0)
                clock[0] = 112.0
            return process(*args, **kwargs)
        self.s.sql_process = sql
        result = self.run_fake()
        self.assert_failed_clean(result)
        self.assertEqual(result['activity_census']['status'], 'SKIPPED_BUDGET')
        self.assertEqual(result['activity_census']['read_outcome'], 'NOT_ATTEMPTED')
        self.assertIsNone(result['activity_census']['observation'])
        self.assertEqual(self.census_calls(), [])
        self.s.collect_activity_census(result)
        self.assertEqual(self.census_calls(), [])

    def test_preparation_and_error_record_failures_do_not_replace_original_failure(self):
        for point in ('query', 'record'):
            case = CensusTests('runTest'); case.setUp()
            try:
                def error(*a, **kw): raise RuntimeError('PRIVATE_LOCAL_FAILURE')
                case.s.provision_helper.activity_census_query = error
                if point == 'record': case.s.record_activity_census_error = error
                result = case.run_fake()
                case.assert_failed_clean(result)
                self.assertEqual(result['failure_code'], 'PROVISION_FAILED')
                self.assertNotIn('PRIVATE', json.dumps(result))
                self.assertEqual(case.census_calls(), [])
                if point == 'query':
                    self.assertEqual(result['activity_census']['status'], 'LOCAL_DIAGNOSTIC_FAILED')
                    self.assertEqual(result['activity_census']['read_outcome'], 'NOT_ATTEMPTED')
                else: self.assertNotIn('activity_census', result)
            finally:
                case.tearDown()

    def test_strict_decoder_independent_mutations_and_no_raw_return(self):
        validator = self.helper.validate_activity_census_wire
        self.assertEqual(m.decode_activity_census(m.canonical(census_wire()), validator), census_wire())
        mutations = [lambda v: v.update(PRIVATE='PRIVATE'), lambda v: v.update(status='PRIVATE'),
            lambda v: v['observation'].update(rows_total=True), lambda v: v['observation'].update(rows_total=1.0),
            lambda v: v['observation'].update(rows_total='0'), lambda v: v['observation'].update(rows_total=-1),
            lambda v: v['observation'].update(rows_total=1), lambda v: v['observation']['bins'].pop(),
            lambda v: v['observation']['bins'].reverse(), lambda v: v['observation']['bins'][0].update(backend_category='PRIVATE'),
            lambda v: v['observation']['bins'][0].update(count=False), lambda v: v['observation']['bins'][0].update(count=4097),
            lambda v: v['observation']['bins'][0].update(role_oid_present=0),
            lambda v: v['observation']['bins'][0].update(counted_by_original_predicate=True),
            lambda v: v['observation']['bins'].__setitem__(1, v['observation']['bins'][0])]
        for mutate in mutations:
            value = census_wire(); mutate(value)
            with self.assertRaises(m.CensusError) as caught:
                m.decode_activity_census(m.canonical(value), validator)
            self.assertNotIn('PRIVATE', str(caught.exception))
        for raw in (b'{"a":1,"a":2}', b'{"a":Infinity}', b'{"a":-Infinity}', b'{"a":NaN}', b'\xff', b'[]'):
            with self.assertRaises(m.CensusError): m.decode_activity_census(raw, validator)
        with self.assertRaises(m.CensusError) as caught:
            m.decode_activity_census(m.canonical(census_wire()), lambda value: {'PRIVATE': 'x' * (m.CENSUS_MAX_BYTES + 1)})
        self.assertEqual(caught.exception.code, 'JSON_SIZE_LIMIT')
        self.assertEqual(self.executor.calls, [])


if __name__ == '__main__':
    unittest.main(verbosity=2)
