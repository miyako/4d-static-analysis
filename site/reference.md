---
layout: default
title: Reference
lead: Command-line options, configuration, outputs, exit codes and supported platforms.
permalink: /reference/
---

## Command line

```sh
python3 -m sonar4d ROOT --output NEW_FOLDER [options]     # or: sonar-4d, once installed
python3 -m sonar4d.ci ROOT --output NEW_FOLDER [options]  # for CI: findings exit 0
```

`ROOT` is the repository or project folder. Every `.4dm` file under it is analyzed, except `.git` and excluded paths.
Paths in the reports are relative to `ROOT`.

| Option | Meaning |
| --- | --- |
| `--output` | A **new** folder for the reports. It must not exist and must be outside `ROOT`. |
| `--config` | A JSON configuration file. Nothing is read implicitly. |
| `--native-tool` | The tool4d executable. Turns on the native backend. |
| `--project` | The `.4DProject`, relative to `ROOT`. Required with `--native-tool`. |
| `--native-timeout` | Seconds to wait for tool4d's complete answer. The default is 15, and the maximum is 300. |
| `--leave-unopened` | A source file not sent to tool4d as opened; used for coverage checks. Can be repeated. |
| `--no-dependencies` | Don't install declared components. A project that declares any is then incomplete. |
| `--offline-dependencies` | Install components from verified, cached archives only, with no network access. |
| `--version` | Print the version. |

**Environment:** `SONAR4D_GITHUB_TOKEN` gives a token for private GitHub components. It is passed only to the
dependency installer and never written to reports.

### Provisioning tool4d

```sh
python3 -m sonar4d.provision --destination NEW_FOLDER [--archive CACHED_ARCHIVE]
```

The provisioner downloads the pinned public tool4d for this host and checks it before use:

| Host | Archive | Checks |
| --- | --- | --- |
| macOS ARM64 | `21.0/latest/mac/tool4d_arm64.tar.xz` | SHA-256 pin, `codesign` requirement for 4D's Team ID |
| Windows x64 | `21.0/latest/win/tool4d_win.tar.xz` | SHA-256 pin, valid Authenticode signature from 4D |

It also checks that the version is `21.0 build 21.100422`. It then prints a JSON record whose `toolPath` is the
executable to pass to `--native-tool`. If 4D replaces the "latest" archive, the pin no longer matches and nothing is
installed until the pin is updated.

## Configuration

```json
{
  "rules": {"4D-M001": {"enabled": true, "maxLines": 200}},
  "exclude": ["Vendor/*", "**/Generated/*"]
}
```

| Key | Meaning |
| --- | --- |
| `rules.4D-M001.enabled` | Turn the file-length rule on or off. On by default. |
| `rules.4D-M001.maxLines` | Report files with **more** physical lines than this (1–100000, 200 by default). Blank lines and comments count. |
| `exclude` | Patterns matched against `ROOT`-relative POSIX paths. `*` can match `/`. |

Unknown keys and invalid values are errors.

## Rules

| Rule | Category | Source |
| --- | --- | --- |
| `4D-N001` | Reliability | A tool4d compiler diagnostic, with tool4d's severity (error, warning or note) and message. |
| `4D-M001` | Maintainability | The file is longer than `maxLines`. Reported once per file, at line 1. |

## Outputs

| File | Written when | Content |
| --- | --- | --- |
| `analysis.json` | Always | Version, exact scope, configuration, backends, `complete`, errors and findings. |
| `findings.sarif` | Always | SARIF 2.1.0. Incomplete runs set `executionSuccessful: false` and list the errors. |
| `sonar-issues.json` | Complete analyses only | SonarQube generic issue format. |
| `native-observation.json` | With `--native-tool` | tool4d's raw responses, for diagnosis. It contains temporary paths. |

SARIF details:
- **Paths:** repository-relative, percent-encoded.
- **Columns:** counted in UTF-16 code units, one-based.
- **`partialFingerprints["sonar4d/v1"]`:** built from the rule, the path, the message (numbers normalised) and the
  text of the flagged line, never the line number. CI validates every report against the official OASIS SARIF 2.1.0
  schema.

## Exit codes

| Code | `sonar4d` | `sonar4d.ci` |
| --- | --- | --- |
| `0` | Complete, no findings | Complete, with or without findings |
| `1` | Complete, with findings | — |
| `2` | Failure or incomplete | Failure or incomplete |

## Platforms

| Platform | Custom rule | Native diagnostics |
| --- | --- | --- |
| macOS ARM64 | Yes | **Verified**: tool4d 21.0 builds 21.100301 and 21.100422 |
| Windows x64 | Yes | **Verified**: tool4d 21.0 build 21.100422 |
| Linux | Yes | Not verified yet |
| macOS Intel | Yes | Not verified |

On other builds or platforms, native diagnostics may still appear in `native-observation.json`, but the analysis is
reported incomplete, and those findings are not added to the reports.
