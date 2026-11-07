#!/usr/bin/env python3
"""Offline only: fabricated test2json event streams, no SQL or subprocesses."""
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

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
            "host": "127.0.0.1", "port": 55432, "cleanup": "CONFIRMED", "bootstrap_closed": True, "go_invocations": 1,
            "bootstrap_role": "wt_admin_" + "a"*32, "bootstrap_oid": 10, "activity_guard_oid": 17004,
            "admission_schema_oid": 17005, "admission_function_oid": 17006,
            "admission_function_definition_sha256": "7"*64, "admission_sha256": "8"*64}


ROLLBACK_LEAF = "valid_other_grant_pending_rollback"
ROLLBACK_TEST = evidence.TEST + "/" + ROLLBACK_LEAF


def rollback_marker(**changes):
    fields = dict(stage="INVOKE", error_class="SQL_AMBIGUOUS_REFERENCE", invoke_class="SQL_AMBIGUOUS_REFERENCE",
                  rollback="CONFIRMED", rollback_class="NONE")
    fields.update(changes)
    return ("    registration_authority_fixture_test.go:1234: WT_REGISTRATION_ROLLBACK_DIAG_V1 "
            "stage={stage} error_class={error_class} invoke_class={invoke_class} "
            "rollback={rollback} rollback_class={rollback_class}\n").format(**fields)


def failed_rollback(manifest, output=None):
    """Synthetic 18 PASS / one FAIL / 27 not observed, not runtime evidence."""
    events = complete(manifest)
    index = next(i for i, event in enumerate(events) if event.get("Test") == ROLLBACK_TEST and event["Action"] == "run")
    events = events[:index+3]
    events[index+1]["Output"] = rollback_marker() if output is None else output
    events[index+2]["Action"] = "fail"
    events += [{"Action": "fail", "Package": evidence.PACKAGE, "Test": evidence.TEST},
               {"Action": "fail", "Package": evidence.PACKAGE}]
    return events, index+1


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

    def test_go127_output_types_preserve_exact_terminal_evidence(self):
        for output_type in ("frame", "error", "error-continue"):
            events = copy.deepcopy(self.events)
            events[3]["OutputType"] = output_type
            with self.subTest(output_type=output_type):
                raw = encode(events)
                result = evidence.validate_run(raw, self.manifest, binding(raw), "1"*64)
                self.assertEqual(result["leaves"], self.manifest["leaves"])
                self.assertNotIn("OutputType", json.dumps(result))
                self.assertNotIn("redacted fixture line", json.dumps(result))

    def test_go127_output_type_is_strictly_scoped_and_bounded(self):
        for value in (None, True, 1, [], {}, "", "FRAME", "unknown", "secret SQL or credentials"):
            events = copy.deepcopy(self.events)
            events[3]["OutputType"] = value
            with self.subTest(value_type=type(value).__name__), self.assertRaisesRegex(evidence.EvidenceError, "^EVENT_OUTPUT_TYPE$"):
                evidence.validate_events(encode(events), self.manifest)
        for action in ("start", "run", "pass", "fail", "skip", "pause", "cont", "bench", "invented"):
            events = copy.deepcopy(self.events)
            events[3].update(Action=action, OutputType="frame")
            with self.subTest(action=action), self.assertRaisesRegex(evidence.EvidenceError, "^EVENT_OUTPUT_TYPE$"):
                evidence.validate_events(encode(events), self.manifest)
        events = copy.deepcopy(self.events)
        events[3]["OutputType"] = "frame"
        del events[3]["Output"]
        self.rejects(events)
        for value in (None, False, [], {}, "x"*65537):
            events[3]["Output"] = value
            self.rejects(events)
        for key in ("FailedBuild", "Key", "Value", "Path", "unknown"):
            events = copy.deepcopy(self.events)
            events[3].update(OutputType="frame")
            events[3][key] = "secret"
            with self.subTest(key=key), self.assertRaisesRegex(evidence.EvidenceError, "^EVENT_SCHEMA_OR_PACKAGE$"):
                evidence.validate_events(encode(events), self.manifest)

    def test_go127_frame_output_cannot_supply_a_missing_terminal(self):
        events = copy.deepcopy(self.events)
        events[3].update(OutputType="frame", Output=json.dumps(events[4])+"\nPASS\n")
        del events[4]
        self.rejects(events)

    def test_go127_real_shape_failure_still_rejects_with_all_leaf_counts(self):
        events, _ = failed_rollback(self.manifest, "ordinary line\n")
        events.insert(2, {"Action":"output", "Package":evidence.PACKAGE, "Test":evidence.TEST,
                          "OutputType":"frame", "Output":"=== RUN   "+evidence.TEST+"\n"})
        raw = encode(events)
        with self.assertRaisesRegex(evidence.EvidenceError, "^FAILED_OR_SKIPPED_TEST$"):
            evidence.validate_events(raw, self.manifest)
        summary = evidence.failure_summary(raw, self.manifest)
        self.assertEqual(summary["diagnostic"], "FAILED_OR_SKIPPED_TEST")
        self.assertEqual(summary["invalid_events"], 0)
        self.assertEqual(summary["unknown_test_events"], 0)
        self.assertEqual([sum(x["status"] == status for x in summary["leaves"]) for status in
                          ("OBSERVED_PASS", "OBSERVED_FAIL", "NOT_OBSERVED")], [18, 1, 27])

    def test_rollback_marker_is_fixed_diagnostic_only_and_never_acceptance(self):
        events, index = failed_rollback(self.manifest)
        raw = encode(events)
        summary = evidence.failure_summary(raw, self.manifest)
        self.assertEqual(summary["acceptance"], "NOT_ACCEPTED")
        self.assertEqual(summary["diagnostic"], "FAILED_OR_SKIPPED_TEST")
        self.assertEqual(summary["rollback_diagnostic"], {
            "format":"worktool-registration-rollback-diagnostic-v1", "authority":"DIAGNOSTIC_ONLY",
            "leaf":ROLLBACK_LEAF, "stage":"INVOKE", "error_class":"SQL_AMBIGUOUS_REFERENCE",
            "invoke_class":"SQL_AMBIGUOUS_REFERENCE", "rollback":"CONFIRMED", "rollback_class":"NONE"})
        self.assertNotIn(events[index]["Output"], json.dumps(summary))
        self.assertNotIn("fixture_test.go", json.dumps(summary))
        with self.assertRaisesRegex(evidence.EvidenceError, "^FAILED_OR_SKIPPED_TEST$"):
            evidence.validate_events(raw, self.manifest)
        with self.assertRaises(evidence.EvidenceError):
            evidence.validate_run(raw, self.manifest, binding(raw), "1"*64)

    def test_rollback_marker_static_protocol_enumerations(self):
        stages = ("BUILD", "IMPORT", "SEED", "SNAPSHOT_BEFORE", "BEGIN", "INVOKE", "ROLLBACK", "SNAPSHOT_AFTER", "COMPARE")
        statuses = ("NOT_ATTEMPTED", "CONFIRMED", "UNCONFIRMED")
        classes = ("NOT_ATTEMPTED", "UNCLASSIFIED", "NONE", "LOCAL_INVALID", "UNKNOWN_STOP", "INSUFFICIENT_PRIVILEGE",
                   "CONSTRAINT_REJECTION", "SERIALIZATION_FAILURE_NOT_DENIAL", "CANCELLED_NOT_DENIAL",
                   "SQL_AMBIGUOUS_REFERENCE", "SQL_UNDEFINED_COLUMN", "SQL_UNDEFINED_FUNCTION", "SQL_UNDEFINED_TABLE",
                   "SQL_DATATYPE_MISMATCH", "SQL_FEATURE_NOT_SUPPORTED", "SQL_LOCK_NOT_AVAILABLE", "SQL_DEADLOCK",
                   "SQL_UNIQUE_VIOLATION", "SQL_FOREIGN_KEY_VIOLATION", "UNEXPECTED_SQLSTATE",
                   "TRANSPORT_OR_LOCAL_NOT_DENIAL", "UNEXPECTED_RESULT", "SNAPSHOT_MISMATCH")
        self.assertEqual(evidence.ROLLBACK_DIAGNOSTIC_STAGES, frozenset(stages))
        self.assertEqual(evidence.ROLLBACK_DIAGNOSTIC_STATUSES, frozenset(statuses))
        self.assertEqual(evidence.ROLLBACK_DIAGNOSTIC_CLASSES, frozenset(classes))
        for field, values in (("stage", stages), ("rollback", statuses), ("error_class", classes),
                              ("invoke_class", classes), ("rollback_class", classes)):
            for value in values:
                with self.subTest(field=field, value=value):
                    events, _ = failed_rollback(self.manifest, rollback_marker(**{field:value}))
                    result = evidence.failure_summary(encode(events), self.manifest)
                    self.assertEqual(result["rollback_diagnostic"][field], value)

    def test_go127_log_specimen_requires_formal_failed_leaf(self):
        # Captured from the fixed Go 1.27.3 offline specimen, never a DB run.
        specimen = {"Action":"output", "Package":evidence.PACKAGE,
                    "Test":"TestRegistrationAuthorityRollbackDiagnosticLogSpecimen",
                    "Output":"    registration_authority_fixture_test.go:1253: WT_REGISTRATION_ROLLBACK_DIAG_V1 "
                             "stage=SNAPSHOT_BEFORE error_class=UNCLASSIFIED invoke_class=NOT_ATTEMPTED "
                             "rollback=NOT_ATTEMPTED rollback_class=NOT_ATTEMPTED\n"}
        events, index = failed_rollback(self.manifest)
        events[index] = dict(specimen)
        self.assertNotIn("rollback_diagnostic", evidence.failure_summary(encode(events), self.manifest))
        # Reattribution here is a synthetic parser test, not execution evidence.
        events[index]["Test"] = ROLLBACK_TEST
        summary = evidence.failure_summary(encode(events), self.manifest)
        self.assertEqual(summary["rollback_diagnostic"]["stage"], "SNAPSHOT_BEFORE")
        self.assertEqual(summary["diagnostic"], "FAILED_OR_SKIPPED_TEST")

    def test_rollback_marker_requires_unique_run_and_fail(self):
        original, index = failed_rollback(self.manifest)
        variants = []
        for action in ("pass", "skip", "output"):
            events = copy.deepcopy(original)
            events[index+1]["Action"] = action
            variants.append(events)
        variants += [original[:index-1]+original[index:], original[:index+1]+original[index+2:],
                     original[:index-1]+[original[index-1]]+original[index-1:],
                     original[:index+1]+[original[index+1]]+original[index+1:],
                     original[:index-1]+[original[index]]+[original[index-1]]+original[index+1:],
                     original[:index]+[original[index+1]]+[original[index]]+original[index+2:]]
        for events in variants:
            self.assertNotIn("rollback_diagnostic", evidence.failure_summary(encode(events), self.manifest))
        success = copy.deepcopy(self.events)
        out = next(event for event in success if event.get("Test") == ROLLBACK_TEST and event["Action"] == "output")
        out["Output"] = rollback_marker()
        self.assertNotIn("rollback_diagnostic", evidence.failure_summary(encode(success), self.manifest))
        accepted = evidence.validate_events(encode(success), self.manifest)
        self.assertNotIn("rollback_diagnostic", accepted)
        # A valid-looking marker cannot substitute for a leaf run or terminal.
        for action in ("run", "pass"):
            incomplete = [event for event in success if not (event.get("Test") == ROLLBACK_TEST and event["Action"] == action)]
            self.rejects(incomplete)

    def test_rollback_marker_wrong_event_envelope_is_suppressed(self):
        changes = ({"Package":"secret/foreign"}, {"Test":evidence.TEST}, {"Test":evidence.TEST+"/issuer_import_exact_grant"},
                   {"Test":"secret unknown leaf"}, {"Action":"pass"}, {"Action":"run"}, {"Action":"invented"},
                   {"OutputType":"frame"}, {"OutputType":"error"}, {"OutputType":"error-continue"},
                   {"OutputType":""}, {"OutputType":None}, {"OutputType":[]}, {"Unknown":"secret"},
                   {"Time":5}, {"Elapsed":True}, {"Elapsed":-1}, {"Output":None}, {"Output":[]},
                   {"Output":"secret"*12000})
        for change in changes:
            with self.subTest(keys=list(change)):
                events, index = failed_rollback(self.manifest)
                events[index].update(change)
                result = evidence.failure_summary(encode(events), self.manifest)
                self.assertNotIn("rollback_diagnostic", result)
                self.assertNotIn("secret", json.dumps(result))
        for key in ("Package", "Test", "Action", "Output"):
            events, index = failed_rollback(self.manifest)
            del events[index][key]
            self.assertNotIn("rollback_diagnostic", evidence.failure_summary(encode(events), self.manifest))

    def test_rollback_marker_rejects_unknown_actions_anywhere(self):
        for action in ("unknown", "pause", "cont", "bench", None, False, 3, [], {}):
            events, _ = failed_rollback(self.manifest)
            # An unrelated ordinary output event must not hide bad stream shape.
            events[3]["Action"] = action
            with self.subTest(action_type=type(action).__name__):
                summary = evidence.failure_summary(encode(events), self.manifest)
                self.assertNotIn("rollback_diagnostic", summary)
                self.assertEqual(summary["acceptance"], "NOT_ACCEPTED")
                self.assertEqual(summary["diagnostic"], "UNEXPECTED_EVENT_ACTION")

    def test_rollback_marker_exact_line_and_unknown_values_fail_closed(self):
        line = rollback_marker()
        wrong_lines = [line.rstrip("\n"), line+"secret", "secret"+line, line+"\n", line.replace("\n", "\r\n"),
                       line.replace("    ", "\t", 1), line.replace("fixture_test.go", "other.go"),
                       line.replace("registration_authority_fixture_test.go", "/secret/registration_authority_fixture_test.go"),
                       line.replace(":1234:", ":0:"), line.replace(":1234:", ":01234:"), line.replace(":1234:", ":1234567:"),
                       line.replace(" stage=", " extra=secret stage="), line.replace(" stage=", "\nstage="),
                       line.replace("WT_REGISTRATION_ROLLBACK_DIAG_V1", "WT_REGISTRATION_ROLLBACK_DIAG_V2")]
        for field in ("stage", "error_class", "invoke_class", "rollback", "rollback_class"):
            wrong_lines.extend(rollback_marker(**{field:value}) for value in ("UNKNOWN_CODE", "secret DSN", "", "none"))
        for output in wrong_lines:
            events, _ = failed_rollback(self.manifest, output)
            result = evidence.failure_summary(encode(events), self.manifest)
            self.assertNotIn("rollback_diagnostic", result)
            self.assertNotIn("secret", json.dumps(result))

    def test_rollback_marker_duplicates_and_mixed_forgery_are_suppressed(self):
        for extra in (rollback_marker(), rollback_marker(error_class="UNKNOWN_CODE"),
                      "secret "+rollback_marker(), rollback_marker()+rollback_marker(),
                      rollback_marker().replace("_DIAG_V1", "_DIAG_V2")):
            events, index = failed_rollback(self.manifest)
            duplicate = dict(events[index], Output=extra)
            events.insert(index+1, duplicate)
            self.assertNotIn("rollback_diagnostic", evidence.failure_summary(encode(events), self.manifest))
        events, index = failed_rollback(self.manifest)
        events.insert(index, dict(events[index], Test=evidence.TEST+"/issuer_import_exact_grant"))
        self.assertNotIn("rollback_diagnostic", evidence.failure_summary(encode(events), self.manifest))
        # No reassembly of fragmented/multiline Output is permitted.
        events, index = failed_rollback(self.manifest)
        first, second = events[index]["Output"].split(" error_class=", 1)
        events[index]["Output"] = first
        events.insert(index+1, dict(events[index], Output=" error_class="+second))
        self.assertNotIn("rollback_diagnostic", evidence.failure_summary(encode(events), self.manifest))

    def test_rollback_marker_collection_failure_keeps_original_summary(self):
        events, _ = failed_rollback(self.manifest)
        raw = encode(events)
        expected = evidence.failure_summary(raw, self.manifest)
        del expected["rollback_diagnostic"]
        for error in (RuntimeError("secret collector failure"), KeyboardInterrupt("secret interrupt"),
                      SystemExit("secret exit"), GeneratorExit("secret generator exit")):
            with mock.patch.object(evidence, "_rollback_failure_diagnostic", side_effect=error):
                self.assertEqual(evidence.failure_summary(raw, self.manifest), expected)
        for malformed in (b"not-json\n", b'{"Output":"secret","Output":"other"}\n'):
            result = evidence.failure_summary(raw+malformed, self.manifest)
            self.assertNotIn("rollback_diagnostic", result)
            self.assertNotIn("secret", json.dumps(result))
        for suffix in (b" \n"*evidence.MAX_EVENT_LINES, b"x"*131073,
                       b"x"*(evidence.MAX_EVENTS_BYTES+1)):
            self.assertNotIn("rollback_diagnostic", evidence.failure_summary(raw+suffix, self.manifest))

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
                   "go_invocations": 2, "run_id": "not_synthetic", "command_sha256": "0"*64, "bootstrap_role": "postgres", "bootstrap_oid": True,
                   "activity_guard_oid": 17002, "admission_schema_oid": 0, "admission_function_oid": True,
                   "admission_function_definition_sha256": "0"*64, "admission_sha256": "bad"}
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
