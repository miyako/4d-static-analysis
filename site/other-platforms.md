---
layout: default
title: Other platforms
lead: The analyzer writes standard SARIF and SonarQube's generic issue format, so most code-quality tools can take its findings.
permalink: /other-platforms/
---

> **Status:** GitHub code scanning is the only integration verified end to end so far. The recipes below follow each
> tool's documented import format; treat them as starting points and check the first results.

The same command runs everywhere. Pick the output the tool understands:

| Tool | Import | File |
| --- | --- | --- |
| [SonarQube Server](#sonarqube-server) | Generic issue import | `sonar-issues.json` |
| [GitLab](#gitlab) | Code Quality report, or SARIF where your plan supports it | `findings.sarif` |
| [Azure DevOps](#azure-devops) | SARIF SAST Scans Tab extension | `findings.sarif` |
| [DefectDojo](#defectdojo) | SARIF import | `findings.sarif` |
| [Your editor](#local-use-and-editors) | A SARIF viewer | `findings.sarif` |

A typical command:

```sh
python3 -m sonar4d.ci . --native-tool "$TOOL4D" --project Project/MyProject.4DProject --output ../report
```

On macOS ARM64 and Windows x64, `python3 -m sonar4d.provision --destination <new folder>` downloads and verifies
tool4d, and prints its `toolPath`. Elsewhere, leave out the native options and only the custom rule runs.

## SonarQube Server

The repository includes a SonarQube plugin that registers the 4D language (`.4dm` files), and the analyzer writes
findings in SonarQube's generic issue format. `sonar-issues.json` is written **only for complete analyses**, so an
incomplete run can't import an empty result as "clean".

1. **Build and install the plugin** from `sonar-plugin/`. See its
   [README](https://github.com/miyako/4d-static-analysis/tree/main/sonar-plugin).
2. **Run the analyzer** with its output outside the project.
3. **Import the report with the scanner:**

```sh
sonar-scanner \
  -Dsonar.projectKey=my-4d-project \
  -Dsonar.sources=Project/Sources \
  -Dsonar.externalIssuesReportPaths=../report/sonar-issues.json
```

`4D-N001` arrives as a reliability issue (bug), and `4D-M001` as maintainability (code smell). Use the generic
report rather than SARIF for SonarQube: its SARIF importer would classify every finding as a security issue.

**Not yet verified:** the import into a running server. The plugin targets SonarQube Server 2025.1 LTA. SonarQube
Cloud doesn't install third-party plugins, so `.4dm` indexing there isn't supported.

## GitLab

GitLab can show findings in merge requests through its **Code Quality** report, available on all tiers. Convert the
SARIF to the Code Quality format with a SARIF converter, and publish it as a report artifact:

```yaml
4d-analysis:
  script:
    - python3 -m sonar4d.ci . --output ../report
    - sarif-converter --type codequality ../report/findings.sarif gl-code-quality-report.json
  artifacts:
    reports:
      codequality: gl-code-quality-report.json
```

**Exit codes in GitLab:** with `sonar4d.ci`, findings exit `0`, so the job succeeds and the report is published; an
incomplete analysis exits `2` and fails the job. GitLab only publishes reports from jobs that succeed.

**Not yet verified:** the converter step, and GitLab's own SARIF ingestion. Check the current GitLab documentation for
your tier. For native diagnostics, the job needs a macOS ARM64 or Windows x64 runner.

## Azure DevOps

Microsoft's **SARIF SAST Scans Tab** extension shows SARIF files that a pipeline publishes as a build artifact named
`CodeAnalysisLogs`:

```yaml
- script: python -m sonar4d.ci . --output $(Agent.TempDirectory)/report
- task: PublishBuildArtifacts@1
  inputs:
    PathtoPublish: $(Agent.TempDirectory)/report/findings.sarif
    ArtifactName: CodeAnalysisLogs
```

**Not yet verified** with this analyzer.

## DefectDojo

DefectDojo imports SARIF directly: upload `findings.sarif` with the **SARIF** scan type, from the UI or through its
API. The `partialFingerprints` in each result let DefectDojo match findings across imports.

**Not yet verified** with this analyzer.

## Local use and editors

The analyzer runs on a developer machine as well:

```sh
python3 -m pip install "git+https://github.com/miyako/4d-static-analysis@<commit-sha>"
sonar-4d . --output ../report-001
```

Open `../report-001/findings.sarif` in a SARIF viewer, such as Microsoft's SARIF Viewer extension for VS Code, to jump
from each finding to its line. Each run needs a new output folder, so an old report is never mistaken for a new one.
