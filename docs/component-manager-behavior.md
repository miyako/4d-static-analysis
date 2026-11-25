# 4D component manager: behavioral specification

This document specifies what the dependency managers in this repository do, so that the Python reference
([`miyako/4D` `fetch_dependencies.py`](https://github.com/miyako/4D/blob/f4aa7338a42fd239db4776e86b3061e309edc32e/.github/actions/dependencies/fetch_dependencies.py),
the reviewed pin used by `sonar4d.dependencies`) and the native `4d-components` executable
([`components/`](../components/README.md)) are one specification, not two interpretations.

**Terminology.** *Python reference* is the pinned `fetch_dependencies.py`. *Native* is the compiled standalone
executable `4d-components` (the `dependency-manager: native` option and the "Native" column below), **not** a
dependency manager inside tool4d, which has none, and not 4D's own Dependency manager, which is part of the 4D
application. "Native analysis" elsewhere in this repository means analysis by tool4d, which is unrelated.

It does **not** invent 4D behavior. Every rule is one of:

| Class | Meaning |
| --- | --- |
| **Observed** | Seen in real 4D / 4D Server / tool4d. The evidence ID (D1, W5, C6a, M1 …) is the one used in the [action README](https://github.com/miyako/4D/blob/v1/.github/actions/dependencies/README.md) and its cited experiments. |
| **Hardening** | An implementation rule added for safety. Not a claim about 4D. Listed in [Intentional differences](#intentional-differences-from-the-python-reference). |
| **Unknown** | Not established. Both implementations report it explicitly (a warning or an incomplete dependency) instead of guessing. |

Every rule names the test that pins it. *Differential* tests (`components/cmd/4d-components/scenarios*_test.go`,
`differential_test.go`) run the unmodified Python reference and the native implementation against the same mock
provider and compare exit codes, reports, lock files, cache and archive-store trees, stderr and the provider
requests; only paths and JSON-parser wording are normalised.

## 1. Manifest

| Rule | Class | Test |
| --- | --- | --- |
| The main manifest is `Project/Sources/dependencies.json`; its top level is an object and `dependencies` is an object. A missing file or a missing `dependencies` key declares nothing. | Observed (docs) | `manifest without dependencies, and none at all` |
| Malformed JSON, an empty file, a non-object top level or a non-object `dependencies` stops the run (exit 2). 4D's handling of malformed manifests is **not observed**; failing explicitly is deliberate. | Unknown → fail closed | `malformed project manifests are usage errors`, `dependencies of the wrong type`, `empty manifest file` |
| A declaration that is not an object (string, `null`, list) is incomplete, never local. | Fail closed | `non-object declarations are incomplete` |
| A component's own manifest (`Project/Sources/dependencies.json` read from its repository at the resolved tag, **not** from the shipped archive) that cannot be parsed or is wrongly shaped makes that component incomplete; one without `dependencies` is valid. | Observed (D8, W7, W8) + fail closed | `unparsable and wrongly shaped child manifests` |
| `environment4d.json` in the package folder or the nearest parent folder overrides names that the main manifest declares. A name declared only there is ignored with a warning. A string entry is a path. | Observed (D17, C4a–C4d) | `environment4d overrides, ignores env-only names, Components overload`, `environment4d string entry is a path` |
| A relative `path` resolves from the folder that contains the project package (also when a local component declares it); a relative `path` in `environment4d.json` resolves from that file's folder. | Observed (R1–R3, R6–R8) | `GitHub children of a local component are resolved; relative paths …`, `environment4d relative paths resolve from its own folder` |
| A `path` declared by a **GitHub** component is not resolved; a warning is recorded. | Unknown | `local path declared by a GitHub component is not resolved` |
| A UTF-8 byte order mark is accepted. | Implementation | `ojson` tests |

## 2. Component identity and repository formats

| Rule | Class | Test |
| --- | --- | --- |
| A GitHub declaration is `{"github": "owner/repo"}` (slashes at the ends are trimmed); the repository must match `[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+`. | Observed | `invalid repositories` |
| A GitLab declaration is `{"gitlab": "group[/subgroup]/project"}` with an optional `"host": "https://<host>"` (https only). | Observed (GL1, GL8) | `a missing project and release` |
| A declaration with neither is a **local** component (`path`, or `{}`). | Observed | local scenarios |
| The component name (the key), the repository and the installation folder are different things: the cache is keyed by repository and tag, `Components/` by name. | Observed | — |
| A repository part, tag, component name or stored file name that is `.`, `..`, empty or contains a separator, `:` or NUL is **rejected** (it would escape the cache). | Hardening | `TestUnsafeNamesAndTagsAreIncomplete` |

## 3. Versions and tags

| Rule | Class | Test |
| --- | --- | --- |
| `tag` is an exact literal; a prefix such as `v` is never rewritten. An exact `version` selects that tag. | Observed (D7, W7) | `latest exact tag and range` |
| No `version`, or `latest`, selects the latest release (GitHub: the API's latest; the lock records `latest`). | Observed (D7, D9) | same |
| Ranges (`^`, `~`, `>=`, `x`, `*`, `a - b`, `\|\|`, space-separated AND) select the highest matching release. Prereleases and drafts are ignored. R tags (`21R5`) are not SemVer and never satisfy a range. | Observed (D7) | `version` unit tests |
| `"version": "4d"` anchors to the **running runtime** (`--runtime-version`), not `compatibilityVersion`: 21R5 → `4D:21R5`, 21.2 → `4D:21`. LTS `N` selects the newest `N.x`; R anchor `NRm` selects the newest `N.x`/`NRk` with `k ≤ m`; with none it selects the newest older major. | Observed (D7, D8, W7, A2) | `4d anchor on 21R5 and 21.2` |
| `"version": "4D:<anchor>"` states the anchor explicitly. Without a runtime version `"4d"` is incomplete. | Observed + fail closed | `4d without a runtime version is incomplete` |
| Tag ordering and version ordering are separate: ranges use numeric `major.minor.patch`; anchors use the `(major, R, a, b)` key with the tag text as the final tiebreak. Neither is imposed on `tag`. | Observed | `version` unit tests |
| GitLab: no `version` means `highest` (highest semantic version); `latest` is GitLab's most recently released, which can differ. | Observed (GL2–GL6, H1–H7) | `versions resolve as in 4D 21 R5` |

## 4. Resolution and transitive dependencies

| Rule | Class | Test |
| --- | --- | --- |
| Resolution is breadth first in declaration order, recursive through the children read at the resolved tag. The lock is flat. | Observed (D8, W7, W8) | `recursion, flat lock, pins and update` |
| A direct declaration wins over a child's declaration of the same name. | Observed (D14, W14) | `direct declaration wins over a child's` |
| Cycles (X→Y→X, X→X, X→Y→Z→X) resolve without error; each component is loaded once; dependencies declared inside the cycle are still resolved. | Observed (Y1–Y4) | `dependency cycles load each component once` |
| Differing child declarations of one name are settled **one at a time, in the order 4D meets them**, each against the current state (initially the first declaration). A `version` declaration combines with a state that also comes from `version` to the highest release satisfying both; `latest` combines with anything; when the state or the declaration is a `tag`, or the state is already combined, the state is kept; when nothing satisfies both the state is kept. A declaration that cannot meet the state is a **conflict**: the dependency is still installed as the state, but every component that declares it is disabled (errCode 21) and reported incomplete. A conflict is never settled by visiting order alone. | Observed (C6a–C6c, L1–L5, F1–F7, T1–T4, V1, V2, E1, A1, A2, G1–G3, M1–M5) | `TestDifferentialCombinedDeclarations` (10 cases) |
| Combining can change the tag and so the children: resolution restarts (at most 64 passes). | Implementation | `a conflict restarts with the combined tag's children` |
| Identical secondary declarations are not combined. | Observed | `identical secondary declarations are not combined` |
| Secondary declarations naming different repositories are incomplete. | Fail closed | `secondary declarations naming different repositories` |
| A local component's own declarations are resolved like a GitHub component's children; GitLab components' own dependencies are **not** followed. | Observed (C6ctl, M1t, M2t) | `GitHub children of a local component …`, `a GitLab component's own dependencies are not followed` |
| Results are deterministic: the same inputs give the same report order, lock and cache. | Implementation | differential order comparison |

## 5. Locks

| Rule | Class | Test |
| --- | --- | --- |
| The 4D-compatible runtime lock is `<package>/userPreferences.<user>/dependencies-lock.json`: top-level `version` (runtime code), flat `dependencies`, entries with `github`/`gitlab`, `version`, `tag`, `htmlURL`, `archiveURL`, `archiveSize`, `path`, `dependencies`, `found`, `loaded`, `errors`. Local paths appear in it. Known differences from real 4D: `loaded` is `null` (unknowable), GitHub entries carry `archiveSize`/`dependencies`, nested `dependencies` are written as declared. | Observed (D1, W1) | lock comparison in every differential step |
| A tag pinned by an unchanged declaration wins even online; `--update` ignores pins; a pin must match the declaration's lock `version` and, for ranges, still satisfy the range. | Observed (D11, D12, W9, W11) | `recursion, flat lock, pins and update`, `a combined pin is reused` |
| Without release metadata the lock entry has no `tag` and empty URLs; a rejected token does the same. | Observed (D15, W15, GL7) | `missing repository and release give an incomplete lock entry` |
| The analyzer keeps the last complete lock per analyzed project in its own cache and replays it. A project-level lock file is never added to the user's repository. | Implementation | `sonar4d` lock tests |
| The portable, deterministic **inventory** (`4d-components inventory`) is a separate contract from the runtime lock: sorted, versioned, no absolute or temporary paths, no secrets. | Implementation | `TestInventoryIsDeterministicPortableAndSecretFree` |

## 6. Cache and archive integrity

| Rule | Class | Test |
| --- | --- | --- |
| 4D's cache is `<user cache>/<runtime>/Dependencies/.github/<owner>/<repo>/<tag>/` (the unwrapped release asset) plus `<tag>.json`; GitLab: `.gitlab/[<host>/]<group>/<project>/<tag>/`. Runtime names: `4D`, `4D Server`, `tool4d`. | Observed (D1, D16, GL1, GL8) | cache tree comparison |
| The GitHub asset must be `<repo>.zip`; GitLab uses the first asset link the API lists, whatever its name. A single `.4dbase` wrapper folder is removed. | Observed (D1, GL1, K1t) | `digest mismatch, missing asset and unknown asset name`, `with several links the first listed is used …` |
| Recognised package forms: old compiled flat `.4DZ`, `Contents/*.4DZ`, interpreted `Project/*.4DProject`. Anything else is not a package. | Observed | `fsx` tests |
| Real 4D reuses a tag folder without verifying it and does not repair it. **Shared cache mode** (`--archive-store none`) does the same, labels reuse `unverified (4D-equivalent)`, and reports a damaged folder incomplete. | Observed (D2, D5, W2, W5) | `shared cache mode reuses unverified …` |
| **Archive store mode** (default with `--home`): every archive is stored with its SHA-256; on every use it is re-hashed and compared with the release digest (online) or the recorded digest (offline) and then **extracted afresh**; a mismatching archive is discarded and downloaded again. Stored metadata is never trusted on its own. | Implementation | `tampered archive is discarded …` |
| A download is checked against the release asset's size and `sha256:` digest **when present**. A GitHub digest can be absent; GitLab supplies none. A locally recorded SHA-256 detects a mismatch with *that record*; it is not publisher authentication and does not stop an attacker who can replace archive and metadata. | Implementation (stated limit) | `digest mismatch, …`, `NoDigest` case |
| Old tags are never removed from the cache. | Observed (D9) | — |
| Concurrent runs sharing a cache are serialised by lock files and install atomically through a staging folder. | Implementation | `TestConcurrentInstallsShareACacheSafely` |

## 7. Archive extraction (untrusted input)

Observed behavior is only "unwrap one `.4dbase`". All of the following is **hardening** shared by both
implementations unless marked *native only*:

* reject absolute paths, `..`, drive letters, backslashes and symbolic links; skip `__MACOSX/` and `.DS_Store`;
* limits: 50 000 entries, 2 GiB extracted (native: enforced on the bytes actually written, not only the declared sizes);
* native only: reject duplicate entries, device/FIFO/other special entries, NUL bytes, and (Windows) `:`;
* native only: a corrupt or non-ZIP archive makes that component incomplete (the Python reference raises
  `BadZipFile` and exits 1);
* extract into a fresh folder, validate the package form, then install atomically; a partial extraction is never complete;
* a downloaded component is never executed.

Tests: `fsx` tests, `unsafe archives are incomplete`.

## 8. Installation

| Rule | Class | Test |
| --- | --- | --- |
| A copy in `Components/` with the same name (`<name>.4dbase`, `<name>.4DZ`, `<name>`) takes precedence; the dependency is still resolved, reported `overloadedBy` with errCode 19 and is **not** incomplete; its children are still resolved. | Observed (D13, W13, C5) | `children of an overloaded dependency are still resolved`, `TestInstallIntoComponentsKeepsPrecedenceAndOriginalProject` |
| tool4d's **startup** mode loads exact-tag cache entries; its **LSP** mode does not, but loads `Components/`. The analyzer therefore installs each resolved package under the **analysis copy's** `Components/<name>.4dbase`. This must not be replaced by cache-only installation without new end-to-end LSP evidence. | Observed | analyzer tests |
| Local components are installed only when their resolved folder is **inside** the analysis workspace, does not contain the analyzed project and exists; anything else is incomplete. | Implementation | `TestInstallLocalComponentsOnlyInsideTheWorkspace` |
| Installation never modifies the original project: callers pass a disposable copy; staging folders prevent a half-installed component being mistaken for a complete one; copying never follows symbolic links (native: a link inside a local component is an error). | Implementation | `TestInstallInto…`, `CopyTree` test |
| Installation is idempotent. | Implementation | `TestInstallIntoComponentsKeepsPrecedence…` |

## 9. Offline

| Rule | Class | Test |
| --- | --- | --- |
| Offline, an exact or pinned tag is served from a verified stored archive; an unpinned `latest`, range or anchor **fails** — there is no fallback to an older cached tag. | Observed (D10) | `offline replays the verified archive and refuses unpinned latest` |
| Offline performs **no network request** (the HTTP layer refuses before connecting). | Implementation | `TestOfflineMakesNoNetworkRequests`, `TestOfflineMakesNoRequest` |
| A tampered or missing stored archive offline is incomplete. | Implementation | `tampered archive …` step 3 |
| A known same-tag archive may be used after a non-authentication lookup outage or a deleted release when it re-hashes against its record, with a warning. A rejected credential is never worked around by an anonymous retry or a cached archive. | Implementation | `a deleted release …`, `an outage falls back …`, `a rejected token is not worked around …` |
| **Bootstrap caveat.** The *Python* path obtains its pinned script before it can honour `--offline`, so a first run may download that script. The native executable needs no bootstrap download: the action builds it from the reviewed source and a local user supplies its path. | Fact | — |

## 10. Authentication, rate limits and runners

* The optional GitHub token comes from `DEPENDENCIES_GITHUB_TOKEN` or a 4D-style `github.json` (`--token-file`), the
  GitLab token from `DEPENDENCIES_GITLAB_TOKEN` / `--gitlab-token-file`. The ambient `GITHUB_TOKEN` and `GH_TOKEN`
  are deliberately ignored. A token is **never accepted as a command-line argument**. (Observed D15, W15, K1t–K3t.)
* The token is sent only to the provider's API host (native: host **and port**); asset downloads follow redirects without it.
  A GitLab token goes to `gitlab.com` only, never to a link that points elsewhere or an unverified custom host.
* 401, or 403 other than rate limiting, is a hard failure with no anonymous retry. (Observed D15, W15.)
* Bounded timeouts (60 s) and attempts (4); 429/5xx honour `Retry-After`; a server-requested delay over 60 s fails
  promptly with a clear reason. Tests: `a long retry delay fails at once`, `provider` tests.
* GitHub-hosted runners must use tool4d; 4D and 4D Server are only for self-hosted runners.
* No credential appears in reports, locks, caches, stores, inventories, logs or subprocess arguments.
  Test: `TestNoCredentialIsPersisted`.

## 11. Conditions that make resolution incomplete

A dependency is incomplete (exit 3, never a clean analysis) when: its repository or release is missing or invisible;
the asset is absent or fails its digest/size check; the archive is unsafe, corrupt or not a package; a cached
package is damaged; a token is rejected; a rate limit exceeds the retry bound; offline resolution needs the
network; a declaration is not an object or a repository/host is invalid; a manifest of the component is
unreadable; a version conflict disables a declaring component; a local component is outside the workspace, unreadable, or (once installed into a workspace) missing — the resolver alone only warns, as 4D
writes `found: false`; `"4d"` has no runtime version; GitLab is used on a runtime that does not load it (21.x LTS
and unobserved lines are incomplete; 22+ only warns). The analyzer additionally keeps its own fail-closed contract:
incomplete dependencies or incomplete native diagnostics exit 2 and never produce a Sonar report.

## Intentional differences from the Python reference

Documented, tested and **not** normalised away by the differential comparison unless stated.

| Area | Python reference | Native | Why |
| --- | --- | --- | --- |
| Corrupt / non-ZIP archive | uncaught `BadZipFile`, exit 1 | component incomplete, exit 3 | fail closed without crashing |
| Duplicate / special ZIP entries | last wins / extracted | rejected | hardening |
| Repository `..`/`.`, tag or name with a separator | joined into cache paths unchecked | rejected as incomplete | path traversal |
| Stored archive file name from metadata | joined unchecked | must be a plain file name | untrusted metadata |
| Token host match | host name | host **and port** | no leakage to another port |
| Extraction size limit | declared sizes | declared sizes and bytes written | lying headers |
| Symbolic link inside a local component | followed by `copytree` | error | cannot escape the workspace |
| Unreadable `<tag>.json` in the shared cache | usage error, exit 2 | component incomplete, exit 3 | both non-success |
| Report | — | adds `schemaVersion`, `archiveSha256`, `declaration`, `declaredBy` per entry | inventory inputs (ignored by the comparison) |
| JSON syntax error wording; program name in messages | Python's | Go's | normalised in the comparison, wording only |
| `--action` (`FD_*`) composite-action mode | yes | no | the analyzer invokes the CLI directly; use the miyako/4D action for it |
| `--github-api-url` | — | yes (https, or http on loopback) | tests, GitHub Enterprise |
| Shared-cache tag folder that exists without `<tag>.json` and is not a package | reported complete | incomplete | review finding: fail-open |
| Release digest with an algorithm other than sha256 | ignored | incomplete (unverifiable claim) | fail closed |
| Digest hex case on reuse | exact match | case-insensitive | a valid upper-case digest no longer forces re-download |
| Token over `http://` (other than loopback) | sent | never sent; https only | no cleartext credential, including after a redirect downgrade |
| GitLab `host` | name only (instances on different ports share a folder; `..` accepted) | host and port key the folder; unsafe names rejected | cache collision / escape |
| More than 1000 releases | silently truncated | incomplete | a wrong tag must not look complete |
| 2xx other than 200 | accepted | failure | partial bodies |
| Network error text | includes redirect URL | cause only | pre-signed URLs stay out of reports |
| Store archive vs. extraction | lock released before extraction | lock held until extraction ends | narrows the hash/extract race (a writer of the store is still out of scope) |
| File locks | delete-on-release, racy stale takeover | owner token, atomic takeover | no deleting another owner's lock |
| Replacing a tag folder | delete old, then move | move old aside, restore on failure; unique staging names | never lose the previous package |

## Unknown or unestablished (do not claim parity)

* Real 4D behavior of a `path` declared by a GitHub component; of malformed manifests; of a GitLab token for a
  self-hosted `host`; of upcoming GitLab releases; GitHub-release cycles, some prerelease/ordering cases, and
  cross-major or mixed-train anchors beyond the observed cases.
* Windows 4D Server and native Intel/AMD hardware.
* The Python/native differential comparison (mock provider) runs on Linux, macOS and Windows in CI; only path
  spellings (HFS and Windows paths) and Windows CRLF line endings are normalised. It uses a mock provider, never a
  live one.
* Real tool4d LSP analysis with the native manager (CI job `native-dependency-manager`, macOS and Windows) is
  verified for one fixture with one GitHub component, online and offline, against the Python manager. Other
  projects, GitLab components and the Linux backend are not covered by a real runtime run.
* Acceptance means the LSP workspace response, not a startup-only `COMPONENT LIST`.
