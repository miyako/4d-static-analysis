# Releasing

Releases are created by the **Release** workflow (`.github/workflows/release.yml`), manually, from `main`. **Signing is
mandatory**: the workflow fails before building if a required secret is missing, and it creates no release unless
every artifact is signed. An unsigned release cannot be produced by this workflow.

## What is signed

| Artifact | Signature | Checked by the workflow |
| --- | --- | --- |
| macOS executables (arm64, amd64) | Developer ID Application, hardened runtime, secure timestamp, **notarized by Apple** | `codesign --verify --strict`, the signing authority must be `Developer ID Application` (ad-hoc is rejected), `notarytool` must report `Accepted` |
| Windows executables (amd64, arm64) | Authenticode, SHA-256, RFC 3161 timestamp | `signtool verify /pa` |
| Every archive and `SHA256SUMS` | Sigstore keyless (`cosign sign-blob`), bundle `<file>.sigstore.json` | produced by the workflow; verify with `cosign verify-blob` |
| Every archive | GitHub build-provenance attestation | `gh attestation verify <file> --repo miyako/4d-static-analysis` |

Linux executables have no platform signature; they are covered by the Sigstore bundle and the attestation. A bare
Mach-O command-line tool cannot carry a stapled notarization ticket; Gatekeeper checks notarization online.

## One-time setup

Create a GitHub **environment** named `release` (Settings, Environments). Restrict it to the `main` branch and,
if you want a human gate, add required reviewers. Add these secrets to it (or to the repository):

| Secret | Content |
| --- | --- |
| `APPLE_CERTIFICATE_P12_BASE64` | the *Developer ID Application* certificate and private key, exported as `.p12`, base64-encoded |
| `APPLE_CERTIFICATE_PASSWORD` | the `.p12` password |
| `APPLE_SIGNING_IDENTITY` | the identity name, for example `Developer ID Application: Name (TEAMID)` |
| `APPLE_NOTARY_KEY_BASE64` | an App Store Connect API key (`.p8`), base64-encoded |
| `APPLE_NOTARY_KEY_ID` | that key's ID |
| `APPLE_NOTARY_ISSUER_ID` | the issuer ID of the App Store Connect team |
| `WINDOWS_CERTIFICATE_PFX_BASE64` | an Authenticode code-signing certificate and private key as `.pfx`, base64-encoded |
| `WINDOWS_CERTIFICATE_PASSWORD` | the `.pfx` password |

Use credentials dedicated to this repository. Rotate them if a workflow run is ever compromised; they are only
exposed to the signing jobs, which run on GitHub-hosted runners, and the keychain and key files are deleted at the
end of each run.

## Releasing

1. Bump the version in `pyproject.toml`, `sonar4d/__init__.py`, `sonar-plugin/pom.xml` (and its README and
   `PluginPackageIT`) and add `docs/release-notes/v<version>.md`. The notes must not describe the binaries as
   unsigned; the workflow refuses notes that say so. Merge this through a pull request.
2. **Test the pipeline first**: run *Release* with `publish = false`. It builds, signs and notarizes and keeps the
   archives as the `dry-run-dist` artifact, creating no tag and no release.
3. Run *Release* with the version and `publish = true`. It creates `v<version>` at the commit it ran on.
4. Check the release: six archives, `SHA256SUMS`, the `.sigstore.json` bundles, and verify one of each kind.

## Status

v0.3.0 was released **before** this requirement and its binaries are unsigned (its release notes say so). The
first signed release will be the next one. This workflow has not yet been run with real certificates, so the
signing steps are unverified until the first dry run.
