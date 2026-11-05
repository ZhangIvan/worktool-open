#!/usr/bin/env python3
"""Pure, bounded exact-leaf acceptance parser. Never executes SQL or a process.

Only the owning supervisor may supply runtime binding from the single compiled
artifact invocation, Docker inspection and confirmed cleanup. A JSON file alone
is not proof of execution or authority. Output payloads are intentionally discarded.
"""
from __future__ import annotations

import hashlib
import json
import math
import os
from pathlib import Path
import re
import stat

PACKAGE = "github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
TEST = "TestRegistrationAuthorityIsolatedPostgreSQL"
SCOPE = "focused-registration-consumer-boundary-v1"
IMAGE = "postgres@sha256:41a369edc91d9bdd7a58be6bc71ea0f8f7a3f49e82818a9f0829021ea7840b7c"
MAX_EVENTS_BYTES = 8 << 20
MAX_EVENT_LINES = 12000
MANIFEST_KEYS = {"format", "scope", "package", "test", "leaves", "source_files", "source_inventory_sha256", "scope_notes"}
BINDING_KEYS = {"format", "run_id", "source_manifest_sha256", "source_before_sha256", "source_after_sha256", "executable_before_sha256", "executable_after_sha256", "command_sha256", "build_command_sha256", "events_sha256", "exit_code", "compile_exit_code", "timed_out", "interrupted", "image", "server_version", "database", "database_oid", "schema_oid", "issuer_oid", "consumer_oid", "routine_definition_sha256", "provisioning_evidence_sha256", "host", "port", "cleanup", "bootstrap_closed", "go_invocations"}


class EvidenceError(ValueError):
    """Fixed redacted reason. Never include an input value in exceptions."""


def _fail(code: str):
    raise EvidenceError(code)


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _digest(value):
    return isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value) is not None and value != "0" * 64


def _pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            _fail("DUPLICATE_JSON_KEY")
        result[key] = value
    return result


def strict_json(raw: bytes):
    try:
        return json.loads(raw.decode("utf-8"), object_pairs_hook=_pairs,
                          parse_constant=lambda _: _fail("NONFINITE_JSON"))
    except EvidenceError:
        raise
    except (UnicodeError, ValueError, TypeError, RecursionError):
        _fail("MALFORMED_JSON")


def validate_manifest(manifest: dict) -> dict:
    if not isinstance(manifest, dict) or set(manifest) != MANIFEST_KEYS:
        _fail("MANIFEST_SCHEMA")
    if (manifest["format"] != "worktool-registration-cases-v1" or
            manifest["scope"] != SCOPE or manifest["package"] != PACKAGE or manifest["test"] != TEST):
        _fail("MANIFEST_IDENTITY")
    leaves = manifest["leaves"]
    if (not isinstance(leaves, list) or not leaves or len(leaves) > 128 or
            any(not isinstance(x, str) or re.fullmatch(r"[a-z][a-z0-9_]{1,100}", x) is None for x in leaves) or
            len(set(leaves)) != len(leaves)):
        _fail("MANIFEST_LEAVES")
    paths = manifest["source_files"]
    if (not isinstance(paths, list) or len(paths) != 341 or
            any(not isinstance(x, str) or len(x) > 512 or x.startswith("/") or "\\" in x or
                any(p in ("", ".", "..", ".git", "node_modules", ".cache", "__pycache__") for p in x.split("/")) or
                any(c in x for c in "\x00\r\n") for x in paths) or paths != sorted(set(paths))):
        _fail("MANIFEST_INVENTORY")
    if not _digest(manifest["source_inventory_sha256"]) or sha256(("\n".join(paths)+"\n").encode()) != manifest["source_inventory_sha256"]:
        _fail("MANIFEST_INVENTORY_HASH")
    notes = manifest["scope_notes"]
    if not isinstance(notes, list) or not notes or any(not isinstance(x, str) or len(x) > 1000 for x in notes):
        _fail("MANIFEST_SCOPE_NOTES")
    return manifest


def load_manifest(path) -> dict:
    try:
        p = Path(path)
        if p.is_symlink() or not p.is_file() or p.stat().st_size > 65536:
            _fail("MANIFEST_FILE")
        raw = p.read_bytes()
    except OSError:
        _fail("MANIFEST_FILE")
    return validate_manifest(strict_json(raw))


def source_digest(root, manifest: dict) -> str:
    """Hash exact reviewed names and bytes; no caches, symlinks or exclusions."""
    validate_manifest(manifest)
    root = Path(root)
    if not root.is_absolute() or root.name != "source" or root.resolve() != root:
        _fail("SOURCE_ROOT")
    actual = []
    nodes = 0
    try:
        for directory, dirs, files in os.walk(root, followlinks=False):
            for name in dirs + files:
                nodes += 1
                path = Path(directory) / name
                info = path.lstat()
                rel = path.relative_to(root).as_posix()
                if (nodes > 1024 or len(rel) > 512 or stat.S_ISLNK(info.st_mode) or
                        name in (".git", "node_modules", ".cache", "__pycache__")):
                    _fail("SOURCE_INVENTORY")
                if name in files:
                    if not stat.S_ISREG(info.st_mode) or info.st_size > 1 << 20:
                        _fail("SOURCE_FILE")
                    actual.append(rel)
        if sorted(actual) != manifest["source_files"]:
            _fail("SOURCE_INVENTORY")
        digest = hashlib.sha256()
        total = 0
        for rel in sorted(actual):
            p = root / rel
            before = p.lstat()
            if not stat.S_ISREG(before.st_mode):
                _fail("SOURCE_FILE")
            with p.open("rb") as stream:
                opened = os.fstat(stream.fileno())
                if (before.st_dev, before.st_ino) != (opened.st_dev, opened.st_ino):
                    _fail("SOURCE_CHANGED_DURING_READ")
                body = stream.read((1 << 20) + 1)
            total += len(body)
            if len(body) > 1 << 20 or total > 16 << 20:
                _fail("SOURCE_BOUNDS")
            digest.update((rel + "\x00" + sha256(body) + "\n").encode())
        return digest.hexdigest()
    except OSError:
        _fail("SOURCE_READ")


def validate_events(raw_events: bytes, manifest: dict) -> dict:
    """Require real run and pass terminal events for every exact named leaf.

    The test2json Output field never contributes evidence, even if it contains
    forged JSON or PASS-looking text. No raw output is returned or logged.
    """
    validate_manifest(manifest)
    if not isinstance(raw_events, bytes) or not raw_events or len(raw_events) > MAX_EVENTS_BYTES:
        _fail("EVENTS_BOUNDS")
    lines = raw_events.splitlines()
    if not lines or len(lines) > MAX_EVENT_LINES:
        _fail("EVENTS_BOUNDS")
    expected = [TEST + "/" + x for x in manifest["leaves"]]
    allowed = {TEST, *expected}
    state = {}
    package_started = package_passed = False
    next_leaf = 0
    for line in lines:
        if not line or len(line) > 131072:
            _fail("EVENT_LINE_BOUNDS")
        event = strict_json(line)
        if (not isinstance(event, dict) or set(event) - {"Time", "Action", "Package", "Test", "Elapsed", "Output"} or
                event.get("Package") != PACKAGE):
            _fail("EVENT_SCHEMA_OR_PACKAGE")
        action = event.get("Action")
        test = event.get("Test")
        if test is not None and (not isinstance(test, str) or test not in allowed):
            _fail("UNEXPECTED_TEST")
        if "Time" in event and not isinstance(event["Time"], str):
            _fail("EVENT_TIME")
        if "Elapsed" in event and (type(event["Elapsed"]) not in (int, float) or not math.isfinite(event["Elapsed"]) or event["Elapsed"] < 0):
            _fail("EVENT_ELAPSED")
        if "Output" in event and (not isinstance(event["Output"], str) or len(event["Output"]) > 65536):
            _fail("EVENT_OUTPUT_BOUNDS")
        if package_passed:
            _fail("EVENT_AFTER_PACKAGE_TERMINAL")
        if action == "start":
            if test is not None or package_started or state:
                _fail("DUPLICATE_OR_MISORDERED_START")
            package_started = True
        elif action == "output":
            if not package_started:
                _fail("OUTPUT_BEFORE_START")
            # Deliberately discard Output. It is not a leaf or runtime binding.
        elif action == "run":
            if not package_started or test is None or test in state:
                _fail("DUPLICATE_OR_MISORDERED_RUN")
            if test == TEST:
                if state:
                    _fail("TOP_LEVEL_ORDER")
            else:
                if state.get(TEST) != "run" or next_leaf >= len(expected) or test != expected[next_leaf]:
                    _fail("LEAF_ORDER_OR_PARENT")
                if next_leaf and state.get(expected[next_leaf-1]) != "pass":
                    _fail("OVERLAPPING_LEAVES")
                next_leaf += 1
            state[test] = "run"
        elif action == "pass":
            if not package_started:
                _fail("TERMINAL_BEFORE_START")
            if test is None:
                if state.get(TEST) != "pass" or next_leaf != len(expected) or any(state.get(x) != "pass" for x in expected):
                    _fail("PACKAGE_WITH_INCOMPLETE_TESTS")
                package_passed = True
            else:
                if state.get(test) != "run":
                    _fail("DUPLICATE_OR_UNSTARTED_TERMINAL")
                if test == TEST and (next_leaf != len(expected) or any(state.get(x) != "pass" for x in expected)):
                    _fail("PARENT_WITH_INCOMPLETE_LEAVES")
                state[test] = "pass"
        elif action in ("fail", "skip"):
            _fail("FAILED_OR_SKIPPED_TEST")
        else:
            _fail("UNEXPECTED_EVENT_ACTION")
    if not package_passed or state.get(TEST) != "pass" or any(state.get(x) != "pass" for x in expected):
        _fail("MISSING_TERMINAL_EVIDENCE")
    return {"package": PACKAGE, "test": TEST, "leaves": list(manifest["leaves"]), "events_sha256": sha256(raw_events)}


def validate_run(raw_events: bytes, manifest: dict, binding: dict, expected_source_sha256: str) -> dict:
    """Validate supervisor evidence only after its confirmed physical teardown.

    expected_source_sha256 must come from the separately reviewed, external
    frozen source-pin input, never from rediscovering current files at runtime.
    """
    if not isinstance(binding, dict) or set(binding) != BINDING_KEYS:
        _fail("BINDING_SCHEMA")
    if binding["format"] != "worktool-registration-run-binding-v1" or not _digest(expected_source_sha256):
        _fail("BINDING_IDENTITY")
    hashes = ("source_manifest_sha256", "source_before_sha256", "source_after_sha256", "executable_before_sha256", "executable_after_sha256", "command_sha256", "build_command_sha256", "events_sha256", "routine_definition_sha256", "provisioning_evidence_sha256")
    if any(not _digest(binding[k]) for k in hashes):
        _fail("BINDING_DIGEST")
    if any(binding[k] != expected_source_sha256 for k in ("source_manifest_sha256", "source_before_sha256", "source_after_sha256")):
        _fail("SOURCE_BINDING_MISMATCH")
    if binding["executable_before_sha256"] != binding["executable_after_sha256"] or binding["events_sha256"] != sha256(raw_events):
        _fail("EXECUTABLE_OR_EVENTS_MISMATCH")
    for k, expected in (("exit_code", 0), ("compile_exit_code", 0), ("go_invocations", 1), ("server_version", 180006)):
        if type(binding[k]) is not int or binding[k] != expected:
            _fail("PROCESS_OR_SERVER_STATUS")
    if (binding["timed_out"] is not False or binding["interrupted"] is not False or binding["bootstrap_closed"] is not True or binding["cleanup"] != "CONFIRMED"):
        _fail("RUN_OR_TEARDOWN_UNCONFIRMED")
    if binding["image"] != IMAGE or binding["host"] != "127.0.0.1":
        _fail("TARGET_IDENTITY")
    if not isinstance(binding["run_id"], str) or re.fullmatch(r"synthetic_[a-z0-9_]{8,40}", binding["run_id"]) is None:
        _fail("RUN_IDENTITY")
    if not isinstance(binding["database"], str) or re.fullmatch(r"wt_test_[a-z0-9_]{8,48}", binding["database"]) is None:
        _fail("DATABASE_IDENTITY")
    if type(binding["port"]) is not int or not 1024 <= binding["port"] <= 65535:
        _fail("TARGET_PORT")
    if any(type(binding[k]) is not int or not 0 < binding[k] <= 4294967295 for k in ("database_oid", "schema_oid", "issuer_oid", "consumer_oid")) or binding["issuer_oid"] == binding["consumer_oid"]:
        _fail("TARGET_OIDS")
    result = validate_events(raw_events, manifest)
    return {"format": "worktool-registration-focused-evidence-v1", "status": "PASS_FOCUSED_ISOLATED_SCOPE", "scope": SCOPE, **result,
            "binding": dict(binding), "broader_acceptance": "NOT_PROVEN", "durable_recovery_authority": "NOT_IMPLEMENTED_NOT_PROVEN"}


def failure_summary(raw_events: bytes, manifest: dict) -> dict:
    """Redacted, non-accepting diagnostics even for incomplete/failed streams.

    Only manifest-owned names escape. Observed pass is a test2json terminal,
    never a runtime-acceptance claim. Raw Output and unknown test IDs are dropped.
    """
    validate_manifest(manifest)
    expected = [TEST + "/" + x for x in manifest["leaves"]]
    names = [None, TEST, *expected]
    counts = {name: {"run": 0, "pass": 0, "fail": 0, "skip": 0} for name in names}
    invalid = unknown = 0
    if not isinstance(raw_events, bytes) or len(raw_events) > MAX_EVENTS_BYTES:
        return {"acceptance": "NOT_ACCEPTED", "diagnostic": "EVENTS_BOUNDS", "leaves": [], "parent": "NOT_OBSERVED", "package": "NOT_OBSERVED"}
    lines = raw_events.splitlines()
    if len(lines) > MAX_EVENT_LINES:
        lines = lines[:MAX_EVENT_LINES]
        invalid += 1
    for line in lines:
        if len(line) > 131072:
            invalid += 1
            continue
        try:
            event = strict_json(line)
        except EvidenceError:
            invalid += 1
            continue
        if not isinstance(event, dict) or event.get("Package") != PACKAGE:
            invalid += 1
            continue
        name = event.get("Test")
        if name is not None and not isinstance(name, str):
            invalid += 1
            continue
        if name not in counts:
            unknown += 1
            continue
        action = event.get("Action")
        if action == "start" and name is None:
            counts[None]["run"] += 1
        elif isinstance(action, str) and action in counts[name]:
            counts[name][action] += 1
    def status(name):
        count = counts[name]
        terminal_count = count["pass"] + count["fail"] + count["skip"]
        if terminal_count > 1:
            return "DUPLICATE_TERMINAL"
        if count["run"] > 1:
            return "DUPLICATE_RUN"
        if terminal_count and count["run"] != 1:
            return "TERMINAL_WITHOUT_RUN"
        if not terminal_count:
            return "STARTED_NO_TERMINAL" if count["run"] else "NOT_OBSERVED"
        return next("OBSERVED_" + action.upper() for action in ("pass", "fail", "skip") if count[action])
    try:
        validate_events(raw_events, manifest)
        reason = "EVENTS_COMPLETE_RUNTIME_NOT_ASSESSED"
    except EvidenceError as exc:
        reason = str(exc)
    return {"acceptance": "NOT_ACCEPTED", "diagnostic": reason,
            "leaves": [{"id": leaf, "status": status(TEST + "/" + leaf)} for leaf in manifest["leaves"]],
            "parent": status(TEST), "package": status(None), "invalid_events": invalid, "unknown_test_events": unknown}
