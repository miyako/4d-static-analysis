import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import tempfile
import unittest

from sonar4d.cli import main
from sonar4d.model import Finding
from sonar4d.reports import FINGERPRINT_KEY, sarif

FIXTURES = Path(__file__).parent / "fixtures"
SCHEMA = FIXTURES / "sarif-schema-2.1.0.json"
# Official OASIS SARIF 2.1.0 schema (errata 01), vendored so the tests never fetch it:
# https://docs.oasis-open.org/sarif/sarif/v2.1.0/errata01/os/schemas/sarif-schema-2.1.0.json
SCHEMA_SHA256 = "c3b4bb2d6093897483348925aaa73af03b3e3f4bd4ca38cef26dcb4212a2682e"

try:
    from jsonschema import Draft4Validator
except ImportError:  # optional locally; CI sets SONAR4D_REQUIRE_SCHEMA=1 and installs it
    Draft4Validator = None


def native(path, line, message="if without end if", column=0):
    return Finding(path, line, column, line, column + 2, "4D-N001", message, "reliability", "error")


class Fingerprints(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)

    def write(self, text):
        (self.root / "M.4dm").write_text(text, encoding="utf-8")

    def prints(self, findings):
        report = sarif(findings, True, [], self.root)
        return [r["partialFingerprints"][FINGERPRINT_KEY] for r in report["runs"][0]["results"]]

    def test_moving_code_keeps_the_fingerprint(self):
        self.write("a\nIf (True)\nb\n")
        before = self.prints([native("M.4dm", 2)])
        self.write("\n\n// new comment\na\n    If (True)\nb\n")
        self.assertEqual(self.prints([native("M.4dm", 5)]), before)

    def test_different_code_or_message_changes_the_fingerprint(self):
        self.write("If (True)\nIf (False)\n")
        first, second = self.prints([native("M.4dm", 1), native("M.4dm", 2)])
        self.assertNotEqual(first, second)
        self.write("If (True)\n")
        self.assertNotEqual(self.prints([native("M.4dm", 1, "Missing expression")]), [first])

    def test_identical_findings_are_numbered_in_order(self):
        self.write("If (True)\nx\nIf (True)\n")
        first, second = self.prints([native("M.4dm", 1), native("M.4dm", 3)])
        self.assertEqual(first.split(":")[0], second.split(":")[0])
        self.assertEqual((first.split(":")[1], second.split(":")[1]), ("1", "2"))

    def test_numbers_in_messages_do_not_change_the_fingerprint(self):
        self.write("x := 1\n")
        a = self.prints([native("M.4dm", 1, "expected 3 parameters")])
        b = self.prints([native("M.4dm", 1, "expected 4 parameters")])
        self.assertEqual(a, b)

    def test_file_level_rule_uses_rule_and_path_only(self):
        self.write("line 1\n")
        a = self.prints([Finding("M.4dm", 1, 0, 1, 0, "4D-M001", "Source file has 250 physical lines; maximum 200.")])
        self.write("changed first line\n")
        b = self.prints([Finding("M.4dm", 1, 0, 1, 0, "4D-M001", "Source file has 260 physical lines; maximum 200.")])
        self.assertEqual(a, b)

    def test_unreadable_source_still_fingerprints(self):
        self.assertEqual(len(self.prints([native("missing.4dm", 3)])), 1)


@unittest.skipIf(Draft4Validator is None and not os.environ.get("SONAR4D_REQUIRE_SCHEMA"),
                 "jsonschema is not installed")
class Schema(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if Draft4Validator is None:
            raise AssertionError("SONAR4D_REQUIRE_SCHEMA is set but jsonschema is not installed")
        data = SCHEMA.read_bytes()
        assert hashlib.sha256(data).hexdigest() == SCHEMA_SHA256, "vendored SARIF schema changed"
        cls.validator = Draft4Validator(json.loads(data))

    def assertValid(self, report):
        errors = [f"{list(e.path)}: {e.message}" for e in self.validator.iter_errors(report)]
        self.assertEqual(errors, [])

    def run_cli(self, root, *extra):
        with tempfile.TemporaryDirectory() as directory, contextlib.redirect_stdout(io.StringIO()), \
                contextlib.redirect_stderr(io.StringIO()):
            output = Path(directory) / "report"
            main([str(root), "--output", str(output), *extra])
            return json.loads((output / "findings.sarif").read_text(encoding="utf-8"))

    def test_cli_reports_validate(self):
        self.assertValid(self.run_cli(FIXTURES / "rules"))
        self.assertValid(self.run_cli(FIXTURES / "github", "--config",
                                      str(FIXTURES.parent.parent / ".github/sonar4d-fixture.json")))

    def test_native_and_incomplete_reports_validate(self):
        findings = [native("Project/space #é.4dm", 2), native("Project/space #é.4dm", 2, "Missing expression", 4),
                    Finding("Project/M.4dm", 1, 0, 1, 0, "4D-M001", "Source file has 9 physical lines.")]
        self.assertValid(sarif(findings, True, []))
        self.assertValid(sarif([], False, ["tool4d did not complete"]))


if __name__ == "__main__":
    unittest.main()
