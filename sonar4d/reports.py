import hashlib
from pathlib import Path
import re
from urllib.parse import quote

from . import __version__
from .model import Finding
from .rules import RULES


FINGERPRINT_KEY = "sonar4d/v1"
# Rules that flag a whole file: their fingerprint is the rule and the path only.
FILE_LEVEL_RULES = {"4D-M001"}


def _normalise(text: str) -> str:
    return " ".join(text.split())


def fingerprints(findings: list[Finding], root: Path | None) -> dict[Finding, str]:
    """Stable identities that survive code moving up or down a file (SARIF partialFingerprints).

    A finding is identified by its rule, its path, its message with numbers normalised, and the normalised text of
    the line it flags, never by its line number. Identical keys in one file are told apart by their order."""
    lines: dict[str, list[str]] = {}
    counts: dict[str, int] = {}
    out = {}
    for finding in sorted(set(findings), key=lambda f: (f.path, f.start_line, f.start_column, f.rule_id, f.message)):
        if finding.rule_id in FILE_LEVEL_RULES:
            parts = [finding.rule_id, finding.path]
        else:
            if root is not None and finding.path not in lines:
                try:
                    text = (root / finding.path).read_text(encoding="utf-8-sig")
                    lines[finding.path] = text.replace("\r\n", "\n").replace("\r", "\n").split("\n")
                except (OSError, ValueError):
                    lines[finding.path] = []
            source = lines.get(finding.path) or []
            line = source[finding.start_line - 1] if 0 < finding.start_line <= len(source) else ""
            parts = [finding.rule_id, finding.path, re.sub(r"\d+", "#", _normalise(finding.message)), _normalise(line)]
        key = hashlib.sha256("\0".join(parts).encode("utf-8")).hexdigest()[:32]
        counts[key] = counts.get(key, 0) + 1
        out[finding] = f"{key}:{counts[key]}"
    return out


def sarif(findings: list[Finding], complete: bool, errors: list[str], root: Path | None = None) -> dict:
    identifiers = sorted({finding.rule_id for finding in findings})
    indices = {rule: index for index, rule in enumerate(identifiers)}
    identities = fingerprints(findings, root)
    results = []
    for finding in sorted(set(findings)):
        region = {"startLine": finding.start_line}
        if finding.end_line != finding.start_line:
            region["endLine"] = finding.end_line
        if finding.location_precision == "range" and (
            finding.start_line, finding.start_column
        ) != (finding.end_line, finding.end_column):
            region.update({
                "startColumn": finding.start_column + 1,
                "endLine": finding.end_line,
                "endColumn": finding.end_column + 1,
            })
        results.append({
            "ruleId": finding.rule_id, "ruleIndex": indices[finding.rule_id],
            "level": finding.level, "message": {"text": finding.message},
            "locations": [{"physicalLocation": {
                "artifactLocation": {"uri": quote(finding.path, safe="/"), "uriBaseId": "%SRCROOT%"},
                "region": region,
            }}],
            "partialFingerprints": {FINGERPRINT_KEY: identities[finding]},
        })
    return {
        "$schema": "https://json.schemastore.org/sarif-2.1.0.json",
        "version": "2.1.0",
        "runs": [{
            "tool": {"driver": {
                "name": "sonar-4d", "version": __version__,
                "informationUri": "https://github.com/miyako/4d-static-analysis",
                "rules": [{
                    "id": rule, "name": rule.replace("-", "_"),
                    "shortDescription": {"text": RULES[rule]["name"]},
                    "fullDescription": {"text": RULES[rule]["description"]},
                    "properties": {"tags": [RULES[rule]["category"]]},
                } for rule in identifiers],
            }},
            "columnKind": "utf16CodeUnits",
            "invocations": [{
                "executionSuccessful": complete,
                "toolExecutionNotifications": [
                    {"level": "error", "message": {"text": error}} for error in errors
                ],
            }],
            "results": results,
        }],
    }


def sonar_generic(findings: list[Finding], complete: bool) -> dict:
    if not complete:
        raise ValueError("Refusing to export incomplete analysis as a Sonar generic report")
    rules = []
    for identifier in sorted({finding.rule_id for finding in findings}):
        categories = {f.category for f in findings if f.rule_id == identifier}
        if len(categories) != 1:
            raise ValueError("A Sonar rule must have a consistent category")
        category = categories.pop()
        rules.append({
            "id": identifier, "name": RULES[identifier]["name"],
            "description": RULES[identifier]["description"], "engineId": "sonar-4d",
            "cleanCodeAttribute": "FOCUSED" if category == "maintainability" else "LOGICAL",
            "type": {"maintainability": "CODE_SMELL", "reliability": "BUG",
                     "security": "VULNERABILITY"}[category],
            "severity": "MAJOR",
            "impacts": [{"softwareQuality": category.upper(), "severity": "MEDIUM"}],
        })
    issues = []
    for finding in sorted(set(findings)):
        text_range = {"startLine": finding.start_line}
        if finding.end_line != finding.start_line:
            text_range["endLine"] = finding.end_line
        if finding.location_precision == "range" and (
            finding.start_line, finding.start_column
        ) != (finding.end_line, finding.end_column):
            text_range.update({
                "startColumn": finding.start_column, "endLine": finding.end_line,
                "endColumn": finding.end_column,
            })
        issues.append({
            "ruleId": finding.rule_id,
            "primaryLocation": {"message": finding.message, "filePath": finding.path,
                                "textRange": text_range},
        })
    return {"rules": rules, "issues": issues}
