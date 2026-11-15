---
layout: default
title: 4D static analysis
lead: Check 4D code in CI with the same compiler 4D uses, and report findings where your team already looks — GitHub code scanning, Jenkins, SonarQube and more.
---

## What it does

`4d-static-analysis` is a command-line analyzer for 4D projects. It reads the `.4dm` sources in a repository and
reports findings in standard formats.

It has two backends:

| Backend | What it finds | Needs |
| --- | --- | --- |
| **Native** (`4D-N001`) | The diagnostics of 4D's own compiler: syntax errors, unknown classes or methods, type mismatches. | [tool4d](https://developer.4d.com/docs/Admin/cli#tool4d), downloaded and verified automatically on macOS ARM64 and Windows x64 |
| **Custom** (`4D-M001`) | Source files longer than a configured limit. | Python only, on any OS |

The native backend is the authoritative one. Instead of re-implementing the 4D language, the analyzer loads your
project into tool4d, which runs the same runtime as 4D itself, and collects its diagnostics through the language
server protocol. Declared components are installed first by the
[4D dependencies action](https://miyako.github.io/4D/), so code that calls a component compiles as it would in 4D.

## Outputs

| File | For |
| --- | --- |
| `findings.sarif` | SARIF 2.1.0: GitHub code scanning, Jenkins Warnings NG, Azure DevOps, DefectDojo, SARIF viewers. Includes `partialFingerprints`, so findings keep their identity when code moves. |
| `sonar-issues.json` | SonarQube's generic issue import (complete analyses only). |
| `analysis.json` | The full result: scope, configuration, backends, completion state, errors and findings. |

## It fails safely

An analysis is either **complete** or it isn't. When tool4d doesn't answer for every file, a dependency can't be
installed, or a build isn't verified, the run exits `2` and no "clean" report is produced. A missing result can never
pass for a clean one.

| Exit code | Meaning |
| --- | --- |
| `0` | Complete, no findings |
| `1` | Complete, with findings |
| `2` | Failure or incomplete analysis |

## Where to go next

- [**GitHub code scanning**]({{ '/github/' | relative_url }}): the recommended setup, step by step.
- [**Jenkins**]({{ '/jenkins/' | relative_url }}): a pipeline with the Warnings Next Generation plugin.
- [**Other platforms**]({{ '/other-platforms/' | relative_url }}): SonarQube, GitLab, Azure DevOps, DefectDojo and local use.
- [**Reference**]({{ '/reference/' | relative_url }}): command-line options, configuration, outputs and platforms.

## Status

- **GitHub code scanning:** verified end to end in this repository's CI on macOS and Windows, including GitHub's
  processed alerts.
- **Jenkins, SonarQube, GitLab, Azure DevOps:** the outputs follow each tool's documented format, but these
  integrations haven't been run end to end yet. The pages say so where it matters.
- **Linux:** the custom backend works anywhere; the native backend is not verified on Linux yet.

This is not a security scanner. `4D-N001` and `4D-M001` report compiler diagnostics and a size metric, not
vulnerabilities.
