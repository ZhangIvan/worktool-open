#!/usr/bin/env python3
"""Strict PG evidence adapter. Resource harness and browser hash chain stay unchanged.

--selftest is pure. run delegates one exact pinned resource run; this module
never provisions a database, starts a listener or implements cleanup itself.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import sys
import types

RESOURCE_HARNESS_SHA256 = '88c1ff0e5f63468e883ca96b4a7d35602110d6a21d21f9ea36c093841ee157bf'
SAFE_CODES = frozenset({'RESOURCE_HARNESS_INVALID', 'REPORT_INTEGRITY_FAILURE', 'REQUIRED_CASE_MANIFEST_INVALID', 'INVALID_TEST_EVENT', 'DUPLICATE_TEST_TERMINAL', 'EXTRA_PORT', 'VERSION_STRING_MISMATCH', 'STORE_PRIVILEGED', 'GO_TEST_FAILED', 'RESOURCE_LIMIT_MISMATCH', 'PERSISTENT_OR_FOREIGN_MOUNT', 'SUBPROCESS_NONZERO', 'PG_READY_TIMEOUT', 'RUN_DIRECTORY_ALREADY_EXISTS', 'NON_LOOPBACK_PORT', 'STORE_INHERITS_OWNER', 'UNSAFE_SECRET_DIRECTORY', 'CONNECTION_LIMIT_MISMATCH', 'NON_JSON_TEST_OUTPUT', 'SUBPROCESS_FAILED_OR_TIMEOUT', 'DATABASE_TIMEOUT_MISMATCH', 'NAMESPACE_MISMATCH', 'DATA_DIRECTORY_MISMATCH', 'INVALID_RUN_ID', 'MISSING_FAILED_OR_SKIPPED_INTEGRATION_CASE', 'TMPFS_MISMATCH', 'MISSING_ALLOW', 'CLEANUP_NOT_CONFIRMED', 'CLEANUP_IDENTITY_MISMATCH', 'INVALID_PORT', 'HOSTED_CI_ONLY', 'VERSION_MISMATCH', 'IDENTITY_MISMATCH', 'INVALID_TEST_DSN', 'WAL_OUTSIDE_TMPFS', 'ROLE_OR_TARGET_MISMATCH'})


TESTS = {'TestPGIntegrationSchemaAndDeviceBindings', 'TestPGIntegrationReservationBindings', 'TestPGIntegrationValidMaterializationAndGC', 'TestPGIntegrationInitialInsertForeignKeys'} | {f'TestPGAdmissionATC{i:02d}' for i in range(1, 9)} | {'TestPGAdmissionPayloadStorage', 'TestPGAdmissionPayloadMigrationGuard'} | {f'TestPGLeaseLF{i:02d}' for i in range(1, 9)} | {f'TestPGGatewayGW{i:02d}' for i in range(1, 17)} | {f'TestPGCallbackCB{i:02d}' for i in range(1, 6)}


PACKAGE_ROOT = 'github.com/ZhangIvan/worktool-open/backend-go/internal/'


PACKAGES = frozenset({PACKAGE_ROOT + 'store/pg', PACKAGE_ROOT + 'gateway'})


REQUIRED_CASES_SHA256 = '36fa2c76a55d8d5e23abfccea0f75b2057d27a28003ba77517f215fd2725b5f3'


MAX_REQUIRED_CASES = 512


MAX_TEST_IDENTITIES = 8192


TERMINAL_ACTIONS = frozenset({'pass', 'fail', 'skip'})


EVENT_ACTIONS = frozenset({'start', 'run', 'pause', 'cont', 'pass', 'bench', 'fail', 'output', 'skip'})


def test_package(name):
    return PACKAGE_ROOT + ('gateway' if name.startswith(('TestPGGateway', 'TestPGCallback')) else 'store/pg')


TOP_CASES = frozenset((test_package(name), name) for name in TESTS)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('duplicate JSON field')
        result[key] = value
    return result


def reject_constant(_):
    raise ValueError('nonfinite JSON number')


def strict_json(raw):
    return json.loads(raw, object_pairs_hook=unique_object, parse_constant=reject_constant)


def required_cases():
    # Reviewed local bytes, never a network or request-provided manifest. This
    # hash is part of the existing script identity chain. No secret data lives here.
    try:
        with Path(__file__).with_name('pg-integration-ci.cases.json').open('rb') as f:
            raw = f.read(262145)
        require(len(raw) <= 262144 and hashlib.sha256(raw).hexdigest() == REQUIRED_CASES_SHA256,
                'REQUIRED_CASE_MANIFEST_INVALID')
        value = strict_json(raw)
        require(isinstance(value, dict) and set(value) == {'version', 'cases'} and
                type(value['version']) is int and value['version'] == 1 and
                isinstance(value['cases'], list) and 0 < len(value['cases']) <= MAX_REQUIRED_CASES,
                'REQUIRED_CASE_MANIFEST_INVALID')
        cases = {}
        for row in value['cases']:
            require(isinstance(row, dict) and set(row) == {'package', 'test', 'file'} and
                    all(isinstance(v, str) for v in row.values()), 'REQUIRED_CASE_MANIFEST_INVALID')
            package, name, file = row['package'], row['test'], row['file']
            require(package in PACKAGES and '/' in name and len(name) <= 512 and
                    name.isascii() and all(32 < ord(c) < 127 for c in name) and
                    (package, name.split('/', 1)[0]) in TOP_CASES and
                    re.fullmatch(r'(?:[a-z_]+_)?integration_test\.go', file), 'REQUIRED_CASE_MANIFEST_INVALID')
            key = package, name
            require(key not in cases, 'REQUIRED_CASE_MANIFEST_INVALID')
            cases[key] = file
        return cases
    except (OSError, ValueError, TypeError, KeyError):
        raise RuntimeError('REQUIRED_CASE_MANIFEST_INVALID') from None


def test_event(line):
    try:
        event = strict_json(line)
    except (ValueError, TypeError, UnicodeError):
        raise RuntimeError('NON_JSON_TEST_OUTPUT') from None
    require(isinstance(event, dict), 'INVALID_TEST_EVENT')
    package, action = event.get('Package'), event.get('Action')
    require(isinstance(package, str) and package in PACKAGES and
            isinstance(action, str) and action in EVENT_ACTIONS, 'INVALID_TEST_EVENT')
    if 'Test' in event:
        name = event['Test']
        require(isinstance(name, str) and 0 < len(name) <= 512 and
                all(ord(c) >= 32 and ord(c) != 127 for c in name), 'INVALID_TEST_EVENT')
        top = name.split('/', 1)[0]
        require(top not in TESTS or package == test_package(top), 'INVALID_TEST_EVENT')
    if 'Output' in event:
        require(isinstance(event['Output'], str), 'INVALID_TEST_EVENT')
    return event


def require(ok, code):
    if not ok:
        raise RuntimeError(code)


def result_events(raw):
    required = required_cases()
    terminals = {}
    package_terminals = {}
    for line in raw.splitlines():
        event = test_event(line)
        package, name, action = event['Package'], event.get('Test'), event['Action']
        # Preserve the original fail-closed rule for EVERY observed skip/fail,
        # including tests not in the required manifest and package-level failures.
        require(action not in {'skip', 'fail'}, 'MISSING_FAILED_OR_SKIPPED_INTEGRATION_CASE')
        if action not in TERMINAL_ACTIONS:
            continue
        if name is None:
            require(package not in package_terminals, 'DUPLICATE_TEST_TERMINAL')
            package_terminals[package] = action
        else:
            key = package, name
            require(key not in terminals, 'DUPLICATE_TEST_TERMINAL')
            require(len(terminals) < MAX_TEST_IDENTITIES, 'INVALID_TEST_EVENT')
            terminals[key] = action
    require(set(package_terminals) == PACKAGES and
            TOP_CASES.issubset(terminals) and set(required).issubset(terminals),
            'MISSING_FAILED_OR_SKIPPED_INTEGRATION_CASE')
    # Only fixed reviewed identifiers are emitted, never arbitrary observed names.
    return {'cases': [{'test': n, 'status': 'PASS'} for n in sorted(TESTS)],
            'requiredCases': [{'package': p, 'test': n, 'status': 'PASS'} for p, n in sorted(required)],
            'packages': [{'package': p, 'status': 'PASS'} for p in sorted(PACKAGES)]}


def test_diagnostics(raw):
    # Fixed IDs, source locations and enumerated SQLSTATE only; NEVER Output text.
    failed, states, cases, locations = set(), set(), set(), set()
    terminal = {key: [] for key in TOP_CASES}
    required = required_cases()
    child_terminal = {key: [] for key in required}
    approved_cases = TESTS | {
        'TestPGCallbackCB02/same_delivery_single_claim_and_prepare',
        'TestPGCallbackCB02/revocation_between_claim_and_prepare',
        'TestPGCallbackCB03/unprepared',
        'TestPGCallbackCB03/prepared_unknown',
        'TestPGGatewayGW03/legacy_two_caps',
        'TestPGGatewayGW03/explicit_11_no_fallback',
        'TestPGGatewayGW08/private_consumed_nonce_cas',
        'TestPGGatewayGW08/private_late_command_lock_expiry',
        'TestPGGatewayGW10/cross_tenant',
        'TestPGGatewayGW10/wrong_session',
        'TestPGGatewayGW10/sequence_gap',
        'TestPGGatewayGW10/stale_timestamp',
        'TestPGGatewayGW10/nul_result_and_wrong_attempt',
        'TestPGGatewayGW10/rate_bound',
        'TestPGGatewayGW10/migration_0008_guards',
        'TestPGGatewayGW10/migration_0008_guards/old_check_name_drift',
        'TestPGGatewayGW10/migration_0008_guards/old_check_definition_drift',
        'TestPGGatewayGW10/migration_0008_guards/old_check_missing',
        'TestPGGatewayGW10/migration_0008_guards/result_column_type_drift',
        'TestPGGatewayGW10/migration_0008_guards/expired_but_unrevoked_session',
        'TestPGGatewayGW10/migration_0008_guards/revoked_session_and_result_constraint',
        'TestPGGatewayGW10/migration_0008_guards/revoked_session_and_result_constraint/malformed_json',
        'TestPGGatewayGW10/migration_0008_guards/revoked_session_and_result_constraint/json_null',
        'TestPGGatewayGW10/migration_0008_guards/revoked_session_and_result_constraint/json_array',
        'TestPGGatewayGW10/migration_0008_guards/revoked_session_and_result_constraint/json_string',
        'TestPGGatewayGW10/migration_0008_guards/revoked_session_and_result_constraint/json_number',
        'TestPGGatewayGW10/migration_0008_guards/revoked_session_and_result_constraint/json_boolean',
        'TestPGGatewayGW10/migration_0008_guards/revoked_session_and_result_constraint/sql_null',
        'TestPGGatewayGW10/migration_0008_guards/revoked_session_and_result_constraint/escaped_nul_object',
        'TestPGGatewayGW10/migration_0008_guards/revoked_session_and_result_constraint/empty_object',
    }
    approved_files = {'gateway_integration_test.go', 'migration_guard_integration_test.go', 'ingress_real_integration_test.go', 'callback_vertical_integration_test.go'}
    approved_cases = {(test_package(n), n) for n in approved_cases} | set(required)
    approved_files |= set(required.values())
    approved_states = {'23503', '23505', '23514', '40001', '40P01', '57014', '55P03'}
    for line in raw.splitlines():
        try:
            event = test_event(line)
        except RuntimeError:
            continue
        key = event['Package'], event.get('Test')
        action = event['Action']
        if key in terminal and action in TERMINAL_ACTIONS and len(terminal[key]) < 2:
            terminal[key].append(action)
        if key in child_terminal and action in TERMINAL_ACTIONS and len(child_terminal[key]) < 2:
            child_terminal[key].append(action)
        if action == 'fail' and key in TOP_CASES:
            failed.add(key)
        if action == 'fail' and key in approved_cases:
            cases.add(key)
        output = event.get('Output', '')
        if key in approved_cases:
            states.update(x for x in re.findall(r'\bSQLSTATE=([A-Z0-9]{5})\b', output) if x in approved_states)
            for file, line in re.findall(r'^\s+([a-z_]+\.go):([1-9][0-9]{0,4}):', output, re.MULTILINE):
                if file in approved_files:
                    locations.add((key[0], key[1], file, int(line)))
    return {'failedTests': [n for _, n in sorted(failed)], 'sqlstates': sorted(states),
            'groupOutcomes': [{'package': p, 'test': n, 'terminal': terminal[p, n] or ['NOT_OBSERVED']}
                              for p, n in sorted(TOP_CASES)],
            'requiredCaseOutcomes': [{'package': p, 'test': n, 'terminal': child_terminal[p, n] or ['NOT_OBSERVED']}
                                     for p, n in sorted(required)],
            'failedCases': [{'package': p, 'test': n} for p, n in sorted(cases)],
            'sourceLocations': [{'package': p, 'test': t, 'file': f, 'line': n}
                                for p, t, f, n in sorted(locations)[:64]]}

def resource_harness():
    # Execute exactly the bytes whose hash was checked, avoiding a second read.
    # Importing the pinned module does not invoke its __main__/run/cleanup paths.
    path = Path(__file__).with_name('pg-integration-ci.py')
    try:
        with path.open('rb') as f:
            raw = f.read(262145)
        require(len(raw) <= 262144 and hashlib.sha256(raw).hexdigest() == RESOURCE_HARNESS_SHA256,
                'RESOURCE_HARNESS_INVALID')
        module = types.ModuleType('worktool_pg_pinned_resources')
        module.__file__ = str(path)
        exec(compile(raw, str(path), 'exec'), module.__dict__)
        require(module.TESTS == TESTS and module.SAFE_CODES.issubset(SAFE_CODES), 'RESOURCE_HARNESS_INVALID')
        return module
    except (OSError, ValueError, SyntaxError, AttributeError, TypeError, ImportError):
        raise RuntimeError('RESOURCE_HARNESS_INVALID') from None


def run_with_evidence(engine):
    # Validate local evidence inputs BEFORE the original run can acquire resources.
    required_cases()
    original_result, original_diagnostics = engine.result_events, engine.test_diagnostics
    reports = []

    def checked_result(raw):
        require(not reports, 'REPORT_INTEGRITY_FAILURE')
        try:
            # Conjunction, not replacement: preserve every original result gate.
            original = original_result(raw)
            report = result_events(raw)
            require(original == report['cases'], 'REPORT_INTEGRITY_FAILURE')
        except RuntimeError:
            diagnostic = test_diagnostics(raw)
            # Original run already emitted these diagnostic classes before calling
            # this hook. Add the missing-child/type-failure report only when absent.
            if not diagnostic['failedTests'] and not diagnostic['sqlstates']:
                print(json.dumps({'testDiagnostics': diagnostic}))
            raise
        reports.append(report)
        return original

    engine.result_events, engine.test_diagnostics = checked_result, test_diagnostics
    try:
        engine.run()  # Includes original go exit-code gate and finally cleanup.
    finally:
        engine.result_events, engine.test_diagnostics = original_result, original_diagnostics
    require(len(reports) == 1, 'REPORT_INTEGRITY_FAILURE')
    # An unsuccessful run or its Python cleanup can never reach this success record.
    print(json.dumps({'requiredCaseEvidence': {
        'resourceHarnessSHA256': RESOURCE_HARNESS_SHA256,
        'requiredCasesSHA256': REQUIRED_CASES_SHA256,
        'phase': 'AFTER_RESOURCE_RUN_AND_PYTHON_CLEANUP',
        'requiredCases': reports[0]['requiredCases'], 'packages': reports[0]['packages']}}))


def run():
    run_with_evidence(resource_harness())


def selftest():
    global REQUIRED_CASES_SHA256
    import io
    from contextlib import redirect_stdout
    from unittest.mock import patch
    engine = resource_harness()
    original_invoke = engine.invoke
    def forbidden(*_, **__):
        raise AssertionError('pure selftest attempted subprocess')
    # Preserve all original pure resource/target checks. Its own mock cannot
    # escape to a real subprocess even if an accidental path is added later.
    with patch('subprocess.run', side_effect=forbidden), patch('subprocess.Popen', side_effect=forbidden):
        try:
            engine.selftest()
        finally:
            engine.invoke = original_invoke
    preflight_env, ALLOW = engine.preflight_env, engine.ALLOW
    required = required_cases()
    # Exercise manifest format under a fixture hash, then restore the real pin.
    # Patching local reads cannot start a subprocess, database or network request.
    pinned = REQUIRED_CASES_SHA256
    first = next(iter(sorted(required)))
    row = {'package': first[0], 'test': first[1], 'file': required[first]}
    invalid_manifests = [None, [], {'version': True, 'cases': [row]},
                         {'version': 1, 'cases': []}, {'version': 1, 'cases': [row, row]},
                         {'version': 1, 'cases': [row] * (MAX_REQUIRED_CASES + 1)},
                         {'version': 1, 'cases': [dict(row, package='unapproved/package')]},
                         {'version': 1, 'cases': [dict(row, test='TestUnknown/child')]},
                         {'version': 1, 'cases': [dict(row, file='../secret.go')]},
                         {'version': 1, 'cases': [dict(row, extra='secret')]},
                         {'version': 1, 'cases': [dict(row, test=[])]}]
    invalid_bytes = [json.dumps(v).encode() for v in invalid_manifests]
    invalid_bytes += [b'{"version":1,"version":1,"cases":[]}', b'x' * 262145]
    try:
        for value in invalid_bytes:
            REQUIRED_CASES_SHA256 = hashlib.sha256(value).hexdigest()
            with patch.object(Path, 'open', return_value=io.BytesIO(value)):
                try:
                    required_cases()
                except RuntimeError as exc:
                    assert exc.args == ('REQUIRED_CASE_MANIFEST_INVALID',)
                else:
                    raise AssertionError('invalid manifest accepted')
        REQUIRED_CASES_SHA256 = pinned
        with patch.object(Path, 'open', return_value=io.BytesIO(b'changed')):
            try:
                required_cases()
            except RuntimeError as exc:
                assert exc.args == ('REQUIRED_CASE_MANIFEST_INVALID',)
            else:
                raise AssertionError('manifest hash mismatch accepted')
        with patch.object(Path, 'open', side_effect=OSError('private path secret')):
            try:
                required_cases()
            except RuntimeError as exc:
                assert exc.args == ('REQUIRED_CASE_MANIFEST_INVALID',)
            else:
                raise AssertionError('manifest read failure accepted')
    finally:
        REQUIRED_CASES_SHA256 = pinned
    def encoded(events):
        return b'\n'.join(json.dumps(event).encode() for event in events)
    events = [{'Package': p, 'Test': n, 'Action': 'pass'} for p, n in sorted(TOP_CASES | set(required))]
    events += [{'Package': p, 'Action': 'pass'} for p in sorted(PACKAGES)]
    raw = encoded(events)
    report = result_events(raw)
    assert len(report['cases']) == 43 and len(report['requiredCases']) == len(required)
    assert len(report['packages']) == 2
    def rejected(payload):
        try:
            result_events(payload)
        except RuntimeError as exc:
            assert exc.args[0] in SAFE_CODES
            return
        raise AssertionError('incomplete or invalid evidence accepted')
    # A historical 255-child success cannot certify the 24 new CB03 cases.
    # Fixed names also detect accidental manifest omissions in this matrix.
    recovery_modes = ('zero_start', 'start_before_claim', 'start_after_now',
                      'prepare_before_start', 'prepare_after_now', 'status_present',
                      'error_present', 'empty_error_present', 'leased_delivered',
                      'unprepared_start_equals_claim', 'prepared_equals_start')
    recovery_names = {f'TestPGCallbackCB03/recovery_evidence_{path}_{mode}'
                      for path in ('explicit', 'selector') for mode in recovery_modes}
    recovery_names |= {'TestPGCallbackCB03/recovery_evidence_explicit_unexpired_bad_status',
                       'TestPGCallbackCB03/recovery_evidence_schema_rejects_missing_claim'}
    recovery = {(PACKAGE_ROOT + 'gateway', name) for name in recovery_names}
    assert len(recovery) == 24 and recovery.issubset(required)
    assert all(required[key] == 'callback_recovery_evidence_integration_test.go' for key in recovery)
    # The preceding 279-child success cannot certify the ten new CB02 leaves.
    prepare_modes = ('status_present', 'error_present', 'empty_error_present',
                     'leased_delivered', 'zero_start', 'start_before_claim',
                     'start_after_now', 'start_equals_claim',
                     'sender_status_present', 'sender_start_before_claim')
    prepare = {(PACKAGE_ROOT + 'gateway', f'TestPGCallbackCB02/prepare_evidence_{mode}')
               for mode in prepare_modes}
    assert len(prepare) == 10 and prepare.issubset(required) and prepare.isdisjoint(recovery)
    assert all(required[key] == 'callback_prepare_evidence_integration_test.go' for key in prepare)
    prior = [e for e in events if (e['Package'], e.get('Test')) not in prepare]
    assert sum('/' in e.get('Test', '') for e in prior) == 279
    rejected(encoded(prior))
    historical = [e for e in prior if (e['Package'], e.get('Test')) not in recovery]
    assert sum('/' in e.get('Test', '') for e in historical) == 255
    rejected(encoded(historical))
    # Every exact required child and every original parent is independently mandatory.
    for key in TOP_CASES | set(required):
        rejected(encoded([e for e in events if (e['Package'], e.get('Test')) != key]))
    for key in recovery | prepare:
        for action in ('skip', 'fail'):
            rejected(encoded([dict(e, Action=action) if (e['Package'], e.get('Test')) == key else e for e in events]))
    for package in PACKAGES:
        rejected(encoded([e for e in events if not (e['Package'] == package and 'Test' not in e)]))
    parent_only = [e for e in events if 'Test' not in e or '/' not in e['Test']]
    rejected(encoded(parent_only))
    for key in [next(iter(TOP_CASES)), next(iter(required))]:
        p, n = key
        for action in ['pass', 'skip', 'fail']:
            rejected(raw + b'\n' + encoded([{'Package': p, 'Test': n, 'Action': action}]))
        wrong = next(v for v in PACKAGES if v != p)
        rejected(encoded([dict(e, Package=wrong) if (e['Package'], e.get('Test')) == key else e for e in events]))
    for package in PACKAGES:
        for action in ['pass', 'skip', 'fail']:
            rejected(raw + b'\n' + encoded([{'Package': package, 'Action': action}]))
    p = PACKAGE_ROOT + 'gateway'
    for event in [None, [], 1, 'text', True, {'Package': p, 'Action': 'pass', 'Test': []},
                  {'Package': p, 'Action': []}, {'Package': [], 'Action': 'pass'},
                  {'Action': 'pass', 'Test': 'TestPGCallbackCB02'},
                  {'Package': 'unapproved/package', 'Action': 'pass'},
                  {'Package': p, 'Action': 'unknown'}, {'Package': p, 'Action': 'output', 'Output': {}},
                  {'Package': p, 'Action': 'pass', 'Test': 'x' * 513}]:
        rejected(raw + b'\n' + json.dumps(event).encode())
    for invalid in [b'', b'not-json', b'{"Package":"x","Package":"y","Action":"pass"}',
                    b'{"Package":NaN,"Action":"pass"}', b'\xff']:
        rejected(invalid)
    for action in ['skip', 'fail']:
        rejected(raw + b'\n' + encoded([{'Package': p, 'Test': 'unapproved_synthetic_case', 'Action': action}]))
    # Extra successful tests never become advertised evidence or relaxed requirements.
    extra = result_events(raw + b'\n' + encoded([{'Package': p, 'Test': 'unapproved_synthetic_case', 'Action': 'pass'}]))
    assert extra == report
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
    assert diagnostics['failedTests'] == [] and diagnostics['sqlstates'] == []
    assert diagnostics['failedCases'] == [] and diagnostics['sourceLocations'] == []
    assert len(diagnostics['groupOutcomes']) == 43 and len(diagnostics['requiredCaseOutcomes']) == len(required)
    assert all(v['terminal'] == ['NOT_OBSERVED'] for v in diagnostics['groupOutcomes'] + diagnostics['requiredCaseOutcomes'])
    assert 'secret' not in json.dumps(diagnostics)
    # Exercise sanitization using the newly required source basename too.
    key = next(iter(sorted(prepare)))
    package, name = key
    file = required[key]
    diagnostic_events = [
        {'Package': package, 'Action': 'output', 'Test': name,
         'Output': '    ' + file + ':123: postgresql://secret password body SQLSTATE=23514 SQLSTATE=SECRET\n'},
        {'Package': package, 'Action': 'output', 'Test': name,
         'Output': '    private_secret.go:12: secret\n'},
        {'Package': package, 'Action': 'output', 'Test': 'unapproved_synthetic_case',
         'Output': '    ' + file + ':123: secret SQLSTATE=23503\n'},
        {'Package': package, 'Action': 'fail', 'Test': name},
    ]
    safe = test_diagnostics(encoded(diagnostic_events))
    assert safe['failedCases'] == [{'package': package, 'test': name}]
    assert safe['sourceLocations'] == [{'package': package, 'test': name, 'file': file, 'line': 123}]
    assert safe['sqlstates'] == ['23514']
    assert all(v not in json.dumps(safe) for v in ['secret', 'password', 'postgresql://', 'SQLSTATE='])
    wrong = next(v for v in PACKAGES if v != package)
    spoof = test_diagnostics(encoded([dict(e, Package=wrong) for e in diagnostic_events]))
    assert not spoof['failedCases'] and not spoof['sourceLocations'] and not spoof['sqlstates']
    many = test_diagnostics(encoded([{'Package': package, 'Test': name, 'Action': 'pass'}] * 20))
    indexed = {(row['package'], row['test']): row['terminal'] for row in many['requiredCaseOutcomes']}
    assert indexed[key] == ['pass', 'pass']
    complete = test_diagnostics(raw)
    assert all(v['terminal'] == ['pass'] for v in complete['groupOutcomes'] + complete['requiredCaseOutcomes'])
    malformed = test_diagnostics(b'null\n[]\n{"Test":[],"Action":"fail"}\nnot-json')
    assert not malformed['failedCases'] and not malformed['sourceLocations']
    # Exact original module hash and manifest must fail before resource invocation.
    with patch.object(Path, 'open', return_value=io.BytesIO(b'changed-resource-module')):
        try:
            resource_harness()
        except RuntimeError as exc:
            assert exc.args == ('RESOURCE_HARNESS_INVALID',)
        else:
            raise AssertionError('unverified resource module imported')

    original_run, original_result, original_diag = engine.run, engine.result_events, engine.test_diagnostics
    try:
        for outcome in ['success', 'go_failure', 'cleanup_failure', 'missing_report', 'duplicate_report', 'missing_child']:
            calls = []
            def fake_run():
                calls.append('run')
                try:
                    if outcome == 'missing_report':
                        return
                    engine.result_events(encoded(parent_only) if outcome == 'missing_child' else raw)
                    calls.append('result')
                    if outcome == 'duplicate_report':
                        engine.result_events(raw)
                    if outcome == 'go_failure':
                        raise RuntimeError('GO_TEST_FAILED')
                finally:
                    calls.append('cleanup')
                    if outcome == 'cleanup_failure':
                        raise RuntimeError('CLEANUP_NOT_CONFIRMED')
            engine.run = fake_run
            captured = io.StringIO()
            try:
                with redirect_stdout(captured), patch('subprocess.run', side_effect=forbidden), patch('subprocess.Popen', side_effect=forbidden):
                    run_with_evidence(engine)
            except RuntimeError as exc:
                assert outcome != 'success' and exc.args[0] in SAFE_CODES
            else:
                assert outcome == 'success'
            assert calls[-1] == 'cleanup'
            assert engine.result_events is original_result and engine.test_diagnostics is original_diag
            emitted = [json.loads(line) for line in captured.getvalue().splitlines()]
            proofs = [row for row in emitted if 'requiredCaseEvidence' in row]
            assert len(proofs) == (1 if outcome == 'success' else 0)
            if proofs:
                proof = proofs[0]['requiredCaseEvidence']
                assert proof['phase'] == 'AFTER_RESOURCE_RUN_AND_PYTHON_CLEANUP'
                assert len(proof['requiredCases']) == len(required) and len(proof['packages']) == 2
    finally:
        engine.run, engine.result_events, engine.test_diagnostics = original_run, original_result, original_diag
    print('PASS pure selftest: unchanged resource/target gates; exact package/parent/required-child evidence, missing/duplicate/skip/fail/malformed rejection and bounded redaction; PG NOT_RUN')

if __name__ == '__main__':
    os.umask(0o077)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    signal.signal(signal.SIGINT, lambda *_: sys.exit(130))
    try:
        {'run': run, '--selftest': selftest}[sys.argv[1]]()
    except (Exception, KeyboardInterrupt) as exc:
        code = exc.args[0] if type(exc) is RuntimeError and len(exc.args) == 1 else None
        safe = code if isinstance(code, str) and code in SAFE_CODES else 'UNEXPECTED_REDACTED'
        print('PG_CI_FAILED:' + safe, file=sys.stderr)
        sys.exit(1)
