---
layout: default
title: GitHub code scanning
lead: Show tool4d's compiler diagnostics as code-scanning alerts on every push and pull request.
permalink: /github/
---

The repository root is a GitHub Action. It runs the analyzer on your checkout and uploads the SARIF report with
GitHub's official uploader, so findings appear under **Security → Code scanning** and as annotations on pull requests.
No CodeQL analysis, server or personal token is involved.

## 1. Before you start

- **Code scanning must be available.** It is for public repositories. Private and internal repositories need GitHub
  Code Security enabled.
- **The project is a 4D project folder** with `Project/<Name>.4DProject` and `.4dm` sources.
- **Pin the action to a commit SHA.** The examples use `<commit-sha>`; take one from
  [the commits on `main`](https://github.com/miyako/4d-static-analysis/commits/main).

## 2. Add the workflow

Create `.github/workflows/4d-code-scanning.yml`:

{% raw %}
```yaml
name: 4D code scanning

on:
  push:
  pull_request:

jobs:
  analyze:
    runs-on: macos-15          # or windows-latest
    permissions:
      contents: read
      actions: read
      security-events: write
    steps:
      - uses: actions/checkout@v6
        with:
          persist-credentials: false
      - uses: miyako/4d-static-analysis@<commit-sha>
        with:
          download-tool4d: "true"
          project: Project/MyProject.4DProject
          native-timeout: "60"
          category: 4d-native
```
{% endraw %}

What happens on each run:

1. **tool4d is downloaded** from 4D's public server, without login or licence, and checked against a pinned SHA-256
   and 4D's code signature.
2. **Components are installed.** If the project declares any in `Project/Sources/dependencies.json`, they are resolved
   and installed into a temporary copy of the project. Your checkout is never modified.
3. **tool4d checks every `.4dm` file** in the repository, and the analyzer collects its diagnostics.
4. **The SARIF report is uploaded** to code scanning, but only if the analysis is complete.

## 3. Read the results

- **Alerts:** open **Security → Code scanning**. Each `4D-N001` alert is one compiler diagnostic, at the line tool4d
  reported.
- **Pull requests:** new alerts are annotated on the changed lines.
- **The action's outputs:** `sarif-file` and `analysis-file` (paths on the runner), `sarif-id`, and
  `dependencies-cache-hit`.

A run with findings still succeeds; the findings are the result. A run fails, and uploads nothing, when the analysis
is incomplete: for example, tool4d didn't answer for every file, or a dependency couldn't be installed. That way a
failure can never replace your alerts with a false "clean".

## 4. Choose a runner

| Runner | Native backend | Notes |
| --- | --- | --- |
| `macos-15` | Yes | Apple Silicon. `macos-15-intel` is a different architecture and isn't supported. |
| `windows-latest` | Yes | Windows x64. |
| `ubuntu-latest` | No | Only the custom `4D-M001` rule runs. Leave out `download-tool4d` and `project`. |

To analyze on both macOS and Windows, give each platform its own `category`, for example `4d-native-macos` and
`4d-native-windows`. Without separate categories, each platform's upload replaces the other's.

## 5. Components and tokens

Components declared in `dependencies.json` are installed automatically (`dependencies: install`, the default).

| Input | Use |
| --- | --- |
| `github-token` | Private GitHub components, and higher rate limits. Use a fine-grained, read-only token stored as a secret. It is passed only to the dependency installer. |
| `dependencies: offline` | Install from cached, verified archives only, with no network access. |
| `dependencies: none` | Install nothing. A project that declares dependencies is then reported incomplete. |
| `cache-dependencies` | `"true"` by default: keeps downloaded archives and the last lock between runs with `actions/cache`. Restored archives are re-verified before use. |

{% raw %}
```yaml
      - uses: miyako/4d-static-analysis@<commit-sha>
        with:
          download-tool4d: "true"
          project: Project/MyProject.4DProject
          github-token: ${{ secrets.COMPONENTS_TOKEN }}
```
{% endraw %}

GitLab-hosted components follow 4D's rules: 4D 21.x doesn't load them, so with the current tool4d 21.0 they are
reported incomplete. See the dependency manager's
[GitLab notes](https://miyako.github.io/4D/features/#gitlab-components).

## 6. Limit the scope

The analyzer checks every `.4dm` file under the repository root. To exclude generated or vendored code, or other
projects in the same repository, add a configuration file and pass it as `config`:

```json
{
  "rules": {"4D-M001": {"enabled": true, "maxLines": 400}},
  "exclude": ["Vendor/*", "**/Generated/*"]
}
```

{% raw %}
```yaml
        with:
          config: .github/4d-analysis.json
```
{% endraw %}

Patterns match repository-relative paths, and `*` can match `/`. See the [reference]({{ '/reference/' | relative_url }})
for every option.

## 7. Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| The step fails with "Declared 4D dependencies are incomplete" | A component couldn't be installed. The message names it and says why: for example a private repository without `github-token`, or a local component outside the repository. |
| The step fails with "Observation deadline reached before a complete workspace syntax response" | tool4d didn't report on every file in time. Raise `native-timeout`, or exclude files from other projects with `config`. |
| "Native completion is validated only on macOS ARM64 and Windows x64" | The runner isn't one of the verified platforms. Use `macos-15` or `windows-latest`. |
| "Resource not accessible by integration" on upload | The job is missing `security-events: write`, or code scanning isn't enabled for the repository. |
| Alerts from macOS and Windows replace each other | Give each platform its own `category`. |
