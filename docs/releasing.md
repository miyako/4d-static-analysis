# Releasing

Releases are created by the **Release** workflow (`.github/workflows/release.yml`), manually, from `main`. **Signing is
mandatory**: the workflow fails before building if a required secret is missing, and it creates no release unless
the artifacts are signed as described below. **One item is pending: Windows Authenticode** (see "Pending").

## What is signed

| Artifact | Signature | Checked by the workflow |
| --- | --- | --- |
| macOS executables (arm64, amd64) | Developer ID Application, hardened runtime, secure timestamp, **notarized by Apple** | `codesign --verify --strict`, the signing authority must be `Developer ID Application` (ad-hoc is rejected), `notarytool` must report `Accepted` |
| Windows executables (amd64, arm64) | Authenticode, SHA-256, RFC 3161 timestamp. **Pending**, see below | `signtool verify /pa` (once signed) |
| Every archive and `SHA256SUMS` | Sigstore keyless (`cosign sign-blob`), bundle `<file>.sigstore.json` | produced by the workflow; verify with `cosign verify-blob` |
| Every archive | GitHub build-provenance attestation | `gh attestation verify <file> --repo miyako/4d-static-analysis` |

Linux executables have no platform signature; they are covered by the Sigstore bundle and the attestation. A bare
Mach-O command-line tool cannot carry a stapled notarization ticket; Gatekeeper checks notarization online.

## Secrets

Repository Actions secrets (Settings, Secrets and variables, Actions):

| Secret | Content | Needed for |
| --- | --- | --- |
| `APPLE_DEVELOPER_ID_CERTIFICATE` | the *Developer ID Application* certificate with its private key, exported as `.p12` and base64-encoded | every run |
| `APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD` | the `.p12` password | every run |
| `KEYCHAIN_PASSWORD` | a password for the temporary keychain the run creates | every run |
| `NOTARYTOOL_APPLE_ID` | the Apple ID used for notarization | every run |
| `NOTARYTOOL_PASSWORD` | an app-specific password for that Apple ID | every run |
| `NOTARYTOOL_TEAM_ID` | the Apple Developer team ID | every run |
| `WINDOWS_CERTIFICATE_PFX_BASE64` | an Authenticode code-signing certificate with its private key as `.pfx`, base64-encoded | Windows signing (pending) |
| `WINDOWS_CERTIFICATE_PASSWORD` | the `.pfx` password | Windows signing (pending) |

The signing identity is not a secret: the workflow takes the Developer ID Application identity of the team
`NOTARYTOOL_TEAM_ID` from the certificate. Use credentials
dedicated to this repository and rotate them if a run is ever compromised. They are exposed only to the check and
signing jobs on GitHub-hosted runners, and the keychain and certificate files are deleted at the end of each run.

## Pending: Windows Authenticode

No Authenticode certificate is available yet, so the Windows secrets above are not set. Until they are:

- The workflow warns, skips Windows signing and publishes the Windows executables as
  `4d-components-<version>-windows-<arch>-unsigned.zip`. They are still covered by the Sigstore bundle, the build-provenance
  attestation and `SHA256SUMS`, but carry no Authenticode signature (Windows SmartScreen may warn).
- The release notes **must say that Windows Authenticode signing is pending**; the workflow refuses to publish otherwise.
- As soon as both Windows secrets are set, the workflow signs and verifies the Windows executables automatically, the
  `-unsigned` suffix disappears, and release notes that call any binary unsigned are refused again. Nothing else changes.

## Releasing

1. Bump the version in `pyproject.toml`, `sonar4d/__init__.py`, `sonar-plugin/pom.xml` (and its README and
   `PluginPackageIT`) and add `docs/release-notes/v<version>.md`. The notes must not describe the binaries as
   unsigned; the workflow refuses notes that say so. Merge this through a pull request.
2. **Test the pipeline first**: run *Release* with `publish = false` (any `x.y.z`, for example the current version). It
   builds, signs and notarizes and keeps the archives as the `dry-run-dist` artifact, creating no tag and no release;
   the secrets are still required, but the version files, release notes and tag are checked only when publishing.
3. Run *Release* with the version and `publish = true`. It creates `v<version>` at the commit it ran on.
4. Check the release: six archives, `SHA256SUMS`, the `.sigstore.json` bundles, and verify one of each kind.

## Status

v0.3.0 was released **before** this requirement and its binaries are unsigned (its release notes say so). The next
release will have signed and notarized macOS binaries, Sigstore-signed archives and attestations, and unsigned Windows
executables until Authenticode is set up. A dry run has verified macOS signing and notarization (status `Accepted`),
packaging, the Sigstore signing and the attestation; the Windows signing steps are still untested.
