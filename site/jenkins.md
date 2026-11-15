---
layout: default
title: Jenkins
lead: Run the analyzer in a Jenkins pipeline and show its findings with the Warnings Next Generation plugin.
permalink: /jenkins/
---

> **Status:** this setup follows the documented formats of Jenkins and the Warnings Next Generation plugin, but it
> hasn't been run end to end in Jenkins yet. The analyzer's own behaviour — commands, exit codes and the SARIF it
> writes — is the same as in the verified GitHub setup.

Jenkins needs no special exporter. The analyzer writes standard SARIF 2.1.0, which the
[Warnings Next Generation](https://plugins.jenkins.io/warnings-ng/) plugin reads with its `sarif` parser. The SARIF
includes `partialFingerprints`, which help the plugin tell new findings from existing ones as code moves.

## 1. Prepare the agent

| Agent | What runs |
| --- | --- |
| macOS ARM64 or Windows x64 | Native compiler diagnostics (`4D-N001`) and the custom rule. tool4d is downloaded and verified by the analyzer. |
| Linux, or any other OS | The custom rule (`4D-M001`) only. |

Every agent needs Python 3.11 or later, and `curl` for the tool4d download. Install the analyzer from a reviewed
commit:

```sh
python3 -m pip install "git+https://github.com/miyako/4d-static-analysis@<commit-sha>"
```

## 2. Add a pipeline

This declarative pipeline runs on a macOS agent. It downloads tool4d once per build, analyzes the checkout, and
records the findings.

```groovy
pipeline {
  agent { label 'macos-arm64' }
  stages {
    stage('Provision tool4d') {
      steps {
        sh '''
          rm -rf "$WORKSPACE_TMP/tool4d"
          python3 -m sonar4d.provision --destination "$WORKSPACE_TMP/tool4d" > "$WORKSPACE_TMP/provenance.json"
        '''
      }
    }
    stage('Analyze 4D code') {
      steps {
        sh '''
          TOOL4D=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["toolPath"])' "$WORKSPACE_TMP/provenance.json")
          rm -rf "$WORKSPACE_TMP/report"
          python3 -m sonar4d.ci . \
            --native-tool "$TOOL4D" \
            --project Project/MyProject.4DProject \
            --native-timeout 60 \
            --output "$WORKSPACE_TMP/report"
          cp "$WORKSPACE_TMP/report/findings.sarif" findings.sarif
        '''
      }
    }
  }
  post {
    always {
      recordIssues(tools: [sarif(pattern: 'findings.sarif', id: '4d', name: '4D analysis')])
    }
  }
}
```

Notes on the pipeline:

- **The report goes outside the workspace.** The analyzer refuses to write into the folder it analyzes, so the report
  goes to `$WORKSPACE_TMP`. Only `findings.sarif` is copied back for the plugin.
- **`sonar4d.ci`, not `sonar4d`.** `python3 -m sonar4d.ci` exits `0` when the analysis completes, whether or not it has
  findings, and `2` when it is incomplete. A build with findings therefore stays green, and quality gates decide
  instead. An incomplete analysis fails the build. Use `python3 -m sonar4d` instead if findings should fail the stage
  (exit `1`).
- **Components** declared in `dependencies.json` are installed into a temporary copy. For private GitHub components,
  set `SONAR4D_GITHUB_TOKEN` from a Jenkins credential, using `withCredentials`. The token is passed only to the
  dependency installer.

## 3. Windows agents

On a Windows x64 agent, use `bat` or `powershell` steps:

```groovy
powershell '''
  python -m sonar4d.provision --destination "$env:WORKSPACE_TMP\\tool4d" > "$env:WORKSPACE_TMP\\provenance.json"
  $tool = (Get-Content "$env:WORKSPACE_TMP\\provenance.json" | ConvertFrom-Json).toolPath
  python -m sonar4d.ci . --native-tool $tool --project Project/MyProject.4DProject `
    --native-timeout 60 --output "$env:WORKSPACE_TMP\\report"
  if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  Copy-Item "$env:WORKSPACE_TMP\\report\\findings.sarif" findings.sarif
'''
```

## 4. Quality gates

Warnings NG can fail or mark builds unstable based on the findings, for example when new ones appear:

```groovy
recordIssues(
  tools: [sarif(pattern: 'findings.sarif', id: '4d', name: '4D analysis')],
  qualityGates: [[threshold: 1, type: 'NEW', unstable: true]]
)
```

Native findings are reported as errors, warnings or notes, following tool4d's severity, so you can set gates per
severity too.

## 5. Custom rule only

On agents without tool4d, leave out the provisioning stage and the native options:

```sh
python3 -m sonar4d.ci . --config jenkins/4d-analysis.json --output "$WORKSPACE_TMP/report"
```
