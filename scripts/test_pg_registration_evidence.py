#!/usr/bin/env python3
"""Offline only: fabricated test2json event streams, no SQL or subprocesses."""
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("registration_evidence", HERE / "pg-registration-evidence.py")
evidence = importlib.util.module_from_spec(spec)
spec.loader.exec_module(evidence)


def encode(events):
    return b"".join(json.dumps(x, separators=(",", ":")).encode() + b"\n" for x in events)


def complete(manifest):
    events = [{"Action": "start", "Package": evidence.PACKAGE},
              {"Action": "run", "Package": evidence.PACKAGE, "Test": evidence.TEST}]
    for leaf in manifest["leaves"]:
        name = evidence.TEST + "/" + leaf
        events += [{"Action": "run", "Package": evidence.PACKAGE, "Test": name},
                   {"Action": "output", "Package": evidence.PACKAGE, "Test": name, "Output": "redacted fixture line\n"},
                   {"Action": "pass", "Package": evidence.PACKAGE, "Test": name, "Elapsed": 0.001}]
    return events + [{"Action": "pass", "Package": evidence.PACKAGE, "Test": evidence.TEST},
                     {"Action": "pass", "Package": evidence.PACKAGE}]


def binding(raw):
    return {"format": "worktool-registration-run-binding-v1", "run_id": "synthetic_offline_testing",
            "source_manifest_sha256": "1"*64, "source_before_sha256": "1"*64, "source_after_sha256": "1"*64,
            "executable_before_sha256": "2"*64, "executable_after_sha256": "2"*64,
            "command_sha256": "3"*64, "build_command_sha256": "4"*64, "events_sha256": evidence.sha256(raw),
            "exit_code": 0, "compile_exit_code": 0, "timed_out": False, "interrupted": False,
            "image": evidence.IMAGE, "server_version": 180006, "database": "wt_test_offline_evidence",
            "database_oid": 17000, "schema_oid": 17001, "issuer_oid": 17002, "consumer_oid": 17003,
            "routine_definition_sha256": "5"*64, "provisioning_evidence_sha256": "6"*64,
            "host": "127.0.0.1", "port": 55432, "cleanup": "CONFIRMED", "bootstrap_closed": True, "go_invocations": 1}


class EvidenceTests(unittest.TestCase):
    def setUp(self):
        self.manifest = evidence.load_manifest(HERE / "pg-registration-cases.json")
        self.events = complete(self.manifest)
        self.raw = encode(self.events)

    def rejects(self, events):
        with self.assertRaises(evidence.EvidenceError):
            evidence.validate_events(encode(events), self.manifest)

    def test_complete_exact_events_and_bound_run(self):
        result = evidence.validate_run(self.raw, self.manifest, binding(self.raw), "1"*64)
        self.assertEqual(result["status"], "PASS_FOCUSED_ISOLATED_SCOPE")
        self.assertEqual(result["leaves"], self.manifest["leaves"])
        self.assertEqual(result["broader_acceptance"], "NOT_PROVEN")
        self.assertNotIn("redacted fixture line", json.dumps(result))

    def test_each_missing_run_or_terminal_is_rejected(self):
        for index, event in enumerate(self.events):
            if event["Action"] != "output":
                with self.subTest(index=index):
                    self.rejects(self.events[:index] + self.events[index+1:])

    def test_duplicate_every_terminal_is_rejected(self):
        for index, event in enumerate(self.events):
            if event["Action"] == "pass":
                with self.subTest(index=index):
                    self.rejects(self.events[:index+1] + [event] + self.events[index+1:])

    def test_skip_and_failure_at_leaf_parent_package_rejected(self):
        for index, event in enumerate(self.events):
            if event["Action"] == "pass":
                for action in ("skip", "fail"):
                    events = copy.deepcopy(self.events)
                    events[index]["Action"] = action
                    with self.subTest(index=index, action=action):
                        self.rejects(events)

    def test_extra_leaf_and_package_and_top_rejected(self):
        for key, value in (("Test", evidence.TEST+"/unreviewed_extra"), ("Package", "foreign/package"), ("Test", "TestUnreviewed")):
            events = copy.deepcopy(self.events)
            events[2][key] = value
            self.rejects(events)
        self.rejects(self.events + [{"Action": "output", "Package": evidence.PACKAGE, "Output": "after terminal"}])

    def test_fake_output_cannot_supply_leaf_evidence(self):
        fake = [{"Action": "start", "Package": evidence.PACKAGE},
                {"Action": "run", "Package": evidence.PACKAGE, "Test": evidence.TEST},
                {"Action": "output", "Package": evidence.PACKAGE, "Output": self.raw.decode()},
                {"Action": "pass", "Package": evidence.PACKAGE, "Test": evidence.TEST},
                {"Action": "pass", "Package": evidence.PACKAGE}]
        self.rejects(fake)

    def test_output_never_leaks_sql_credentials_or_raw_rows(self):
        events = copy.deepcopy(self.events)
        secret = "postgresql://user:secret@127.0.0.1 SELECT * FROM private_table"
        events[3]["Output"] = secret
        raw = encode(events)
        result = evidence.validate_run(raw, self.manifest, binding(raw), "1"*64)
        self.assertNotIn(secret, json.dumps(result))
        events[3]["Test"] = secret
        try:
            evidence.validate_events(encode(events), self.manifest)
        except evidence.EvidenceError as exc:
            self.assertNotIn(secret, str(exc))
        else:
            self.fail("foreign output test accepted")

    def test_malformed_duplicate_nonfinite_and_oversized_events(self):
        for raw in (b"not-json", b"{}\n", self.raw + b"\n", b'{"Action":"start","Action":"pass"}\n',
                    b'{"Elapsed":NaN}', b'{"Elapsed":1e999}', b'[]', b'\xff', b'x'*(evidence.MAX_EVENTS_BYTES+1)):
            with self.subTest(size=len(raw)):
                with self.assertRaises(evidence.EvidenceError):
                    evidence.validate_events(raw, self.manifest)

    def test_order_duplicate_run_and_parallel_events_refused(self):
        events = copy.deepcopy(self.events)
        events[2], events[5] = events[5], events[2]
        self.rejects(events)
        self.rejects(self.events[:3] + [self.events[2]] + self.events[3:])
        for action in ("pause", "cont", "bench", "invented"):
            events = copy.deepcopy(self.events)
            events[3]["Action"] = action
            self.rejects(events)

    def test_each_binding_field_is_required(self):
        for field in evidence.BINDING_KEYS:
            bad = binding(self.raw)
            del bad[field]
            with self.subTest(field=field), self.assertRaises(evidence.EvidenceError):
                evidence.validate_run(self.raw, self.manifest, bad, "1"*64)

    def test_fail_closed_binding_changes(self):
        changes = {"source_manifest_sha256": "7"*64, "source_before_sha256": "7"*64, "source_after_sha256": "7"*64,
                   "executable_after_sha256": "7"*64, "events_sha256": "7"*64, "exit_code": 1, "compile_exit_code": 1,
                   "timed_out": True, "interrupted": True, "image": "postgres:18", "server_version": 180005,
                   "database": "production", "host": "localhost", "port": 543, "database_oid": 0,
                   "schema_oid": True, "consumer_oid": 17002, "cleanup": "UNCONFIRMED", "bootstrap_closed": False,
                   "go_invocations": 2, "run_id": "not_synthetic", "command_sha256": "0"*64}
        for field, value in changes.items():
            bad = binding(self.raw)
            bad[field] = value
            with self.subTest(field=field), self.assertRaises(evidence.EvidenceError):
                evidence.validate_run(self.raw, self.manifest, bad, "1"*64)
        with self.assertRaises(evidence.EvidenceError):
            evidence.validate_run(self.raw, self.manifest, binding(self.raw), "8"*64)

    def test_manifest_cannot_omit_or_duplicate_a_leaf(self):
        bad = copy.deepcopy(self.manifest)
        bad["leaves"].append(bad["leaves"][0])
        with self.assertRaises(evidence.EvidenceError):
            evidence.validate_manifest(bad)
        bad = copy.deepcopy(self.manifest)
        bad["leaves"].pop()
        with self.assertRaises(evidence.EvidenceError):
            evidence.validate_events(self.raw, bad)

    def test_inventory_source_hash_and_same_count_substitution(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)/"source"
            root.mkdir()
            for rel in self.manifest["source_files"]:
                path = root/rel
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b"offline fixture")
            before = evidence.source_digest(root, self.manifest)
            target = root/self.manifest["source_files"][0]
            target.write_bytes(b"different offline bytes")
            self.assertNotEqual(before, evidence.source_digest(root, self.manifest))
            target.rename(root/"unreviewed.txt")
            with self.assertRaises(evidence.EvidenceError):
                evidence.source_digest(root, self.manifest)

    def test_failure_summary_is_fixed_named_and_never_accepting(self):
        events = copy.deepcopy(self.events)
        events[4]["Action"] = "fail"
        events[3]["Output"] = "secret PostgreSQL DSN and SQL"
        events.insert(5, {"Action": "fail", "Package": evidence.PACKAGE, "Test": "secret foreign leaf"})
        summary = evidence.failure_summary(encode(events), self.manifest)
        self.assertEqual(summary["acceptance"], "NOT_ACCEPTED")
        self.assertEqual(summary["leaves"][0]["status"], "OBSERVED_FAIL")
        self.assertEqual(summary["unknown_test_events"], 1)
        self.assertNotIn("secret", json.dumps(summary))
        self.assertEqual(evidence.failure_summary(self.raw, self.manifest)["acceptance"], "NOT_ACCEPTED")
        self.assertEqual(evidence.failure_summary(b"", self.manifest)["parent"], "NOT_OBSERVED")
        duplicate = self.events[:5]+[self.events[4]]+self.events[5:]
        self.assertEqual(evidence.failure_summary(encode(duplicate), self.manifest)["leaves"][0]["status"], "DUPLICATE_TERMINAL")

    def test_source_symlinks_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)/"source"
            root.mkdir()
            (root/"linked").symlink_to(directory, target_is_directory=True)
            with self.assertRaises(evidence.EvidenceError):
                evidence.source_digest(root, self.manifest)


if __name__ == "__main__":
    unittest.main()
