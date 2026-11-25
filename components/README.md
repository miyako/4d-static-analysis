# 4d-components

An independent, cross-platform command that resolves, downloads, verifies, caches and installs the component
dependencies declared by a 4D project (`Project/Sources/dependencies.json`, `environment4d.json`). It needs no
Python, no tool4d and no GitHub Actions; it is Go with the standard library only.

It is a faithful port of the reviewed Python manager
([`miyako/4D` `fetch_dependencies.py`](https://github.com/miyako/4D/tree/v1/.github/actions/dependencies)), whose
behavior follows real 4D. **Behavior first, architecture second:** the exact rules, the evidence for them and
every intentional difference are in [`docs/component-manager-behavior.md`](../docs/component-manager-behavior.md);
the language decision is in [`docs/adr/0001-native-component-manager-language.md`](../docs/adr/0001-native-component-manager-language.md).

> **Status.** Opt-in. The analyzer's default manager is still the Python emulation. Use the native one with
> `--dependency-manager native` (action input `dependency-manager: native`). Python stays the fallback until
> parity is accepted on every platform you rely on — see the unestablished list in the behavior document.

## Build

```sh
cd components
go build -trimpath -o 4d-components ./cmd/4d-components     # Go 1.24+, no network needed
go test -race ./...                                          # unit tests, mock HTTP server, no live provider
```

Release builds for `linux/{amd64,arm64}`, `darwin/{arm64,amd64}` and `windows/{amd64,arm64}` are produced with
SHA-256 files by `.github/workflows/components.yml` (workflow artifacts; this repository's release process is
unchanged). Cross-compilation alone is not runtime verification: only the runners that execute the binary
(`native: true` in the matrix) establish that it runs.

## Commands

```text
4d-components resolve   [options] [project]   resolve without installing; downloads nothing, writes nothing
4d-components install   [options] [project]   resolve, download, verify, install into the 4D cache (+ --components-dir)
4d-components inventory [options] [project]   portable, deterministic inventory (add --installed to record SHA-256)
4d-components verify    [options] [project]   offline check of manifests, lock, stored archives, installed components
4d-components cache list|verify|clean [--archive-store DIR] [--all --yes]
4d-components version
```

`project` is the package folder or a `.4DProject` file. The options are the Python reference's, so callers can
switch implementations:

| Option | Meaning |
| --- | --- |
| `--runtime tool4d\|4D\|"4D Server"` | cache folder name (default `tool4d`); only `tool4d` on GitHub-hosted runners |
| `--runtime-version 21R5\|21.2` | anchors `"version": "4d"` (or `--runtime-path` to ask the runtime) |
| `--home DIR` | isolated profile root; the cache goes inside it; the report's `env` says how to point tool4d at it |
| `--dependencies-folder DIR` | explicit `Dependencies` folder |
| `--archive-store DIR\|none` | verified archive store (default on with `--home`); `none` = shared-cache mode |
| `--update` | ignore tags pinned in the last lock |
| `--offline` | no network at all: verified archives and pins only |
| `--no-write-lock`, `--user NAME` | lock file control (`userPreferences.<user>/dependencies-lock.json`) |
| `--rewrite DIR` | write an exact-tag `dependencies.json` into a workspace copy (tool4d startup mode) |
| `--components-dir DIR`, `--workspace DIR` | `install`: also copy every resolved package to `<DIR>/<name>.4dbase`; local components only when inside `--workspace` |
| `--token-file`, `--gitlab-token-file` | `{"token": "..."}` like 4D's `github.json` / `gitlab.json` |
| `--output FILE` | write the JSON report there instead of stdout |
| `--github-api-url URL` | GitHub Enterprise or a test server (https, or http on loopback) |
| `--timeout D`, `--allow-incomplete`, `--quiet` | per-request timeout; exit 0 even if incomplete (explicit opt-in); no stderr diagnostics |

**Credentials.** `DEPENDENCIES_GITHUB_TOKEN` and `DEPENDENCIES_GITLAB_TOKEN` (or the token files). There is
deliberately no `--github-token`: arguments are visible to every user of the machine. `GITHUB_TOKEN` and
`GH_TOKEN` are ignored on purpose. The token is sent only to the provider's API host, never persisted or printed.

**Exit codes** (the same as the Python reference). `0` complete · `1` internal error · `2` invalid invocation or
input, nothing resolved · `3` resolution ran but is **incomplete**. Incomplete is never reported as success; only
`--allow-incomplete` turns it into `0`, and the report still lists what is incomplete.

## Output contracts

* **Report** (`resolve` / `install`): the Python report — `runtime`, `package`, `dependenciesFolder`, `home`,
  `env`, `archiveStore`, `lockFile`, `resolved` (name → tag), `dependencies` (per component: `tag`, `version`,
  `source` `download|archive|cache|local|planned`, `reuse`, `path`, URLs, `dependencies`, `errors`, `error`,
  `overloadedBy`, …), `incomplete`, `warnings` — plus `schemaVersion: 1` and, per entry, `archiveSha256`,
  `declaration` and `declaredBy`; with `--components-dir`, an `installation` object (`installed`,
  `providedByComponents`, `reuse`, `problems`). Under GitHub Actions, `GITHUB_OUTPUT` gets the same outputs as the
  Python script.
* **Lock** (`dependencies-lock.json`): 4D's format (see the behavior document); byte-compatible with the Python
  reference for the same inputs. It can contain local paths. It is a runtime artifact in `userPreferences.<user>/`,
  which the canonical 4D `.gitignore` already ignores; this tool never adds a lock file to a user's repository.
* **Inventory** (`inventory`): `schemaVersion`, `tool`, `runtime`, `complete`, `components[]` sorted by name
  (`source`, `repository`, `host`, `declaration`, `requestedVersion`, `resolved`, `archive{sha256,size,url}`,
  `status` `installed|provided-by-components|planned|local|incomplete`, `cache`, `overloadedBy`, `dependencies`,
  `declaredBy`, `errors`), `errors`, `warnings`. Deterministic for identical inputs; absolute and temporary paths
  are replaced by `<project>`, `<dependencies>`, `<archives>`, `<home>`; no secrets.

## Safety

Downloaded archives are untrusted input and are never executed. They are verified (size and `sha256:` digest when
the release provides one; always re-hashed against the local record on reuse), extracted into a fresh folder with
path/symlink/entry-type/size checks, validated as a 4D package and installed atomically. The original project is
never modified: `install` writes the cache, the lock and (with `--components-dir`) the Components folder you name;
the analyzer passes its disposable copy. A recorded SHA-256 detects change against that record; it is **not**
publisher authentication. `4d-components` is not a sandbox: analyzing untrusted projects with tool4d remains an
OS-isolation concern.

## Layout

```text
cmd/4d-components   CLI (resolve, install, inventory, verify, cache) and the differential/CLI tests
internal/ojson      insertion-ordered JSON, Python-compatible serialisation
internal/version    tags, ranges, runtime anchors (domain; no I/O)
internal/provider   GitHub and GitLab release clients, bounded HTTP, credential rules
internal/fsx        atomic writes, locks, safe extraction, per-platform cache paths
internal/resolver   graph engine, combined declarations, archive store, lock/report
internal/installer  workspace installation and precedence
internal/inventory  portable inventory
internal/testutil   in-memory GitHub/GitLab mock server
testdata/ref_runner.py   runs the unmodified Python reference against the mock (differential tests)
```

## Testing

```sh
go test -race ./...                                   # everything that needs no reference
REF_SCRIPT=/path/to/fetch_dependencies.py go test -run TestDifferential -v ./cmd/4d-components
python -m unittest tests.test_native_manager          # the analyzer's native path (needs Go for the end-to-end tests)
```

The differential tests need the reviewed reference script (commit `f4aa7338…`, SHA-256 `0b9324ba…`; CI fetches and
verifies it) and Python 3. They are skipped, not faked, when it is absent.

## Migration from the Python manager

1. Keep `dependency-manager: python` (default). Nothing changes.
2. Try `dependency-manager: native` / `--dependency-manager native --dependency-manager-path <exe>` next to it. Both
   share the archive store and the analyzer's saved locks, so a cache warmed by one is usable by the other.
3. Compare `incomplete`, `resolved` and the installed `Components/` of both on your projects before switching.
4. Use `python` instead for anything in the "unestablished" list of the behavior document.
