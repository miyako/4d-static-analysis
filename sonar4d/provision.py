"""Install one verified public tool4d archive without credentials or global changes."""

import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import subprocess
import sys
import tarfile
import tempfile

from . import tool4d


SOURCE_URL = (
    "https://resources-download.4d.com/release/21.x/21.0/latest/mac/tool4d_arm64.tar.xz"
)
ARCHIVE_SHA256 = "4a7ef4b26091bf77761956f9b939c83f441902ce77c25952e0eda18a36497b07"
RUNTIME_VERSION = "21.0 build 21.100422"
EXECUTABLE = Path("tool4d.app/Contents/MacOS/tool4d")
SIGNATURE_REQUIREMENT = (
    '=anchor apple generic and certificate leaf[subject.OU] = "37UG5W39Z2" '
    'and identifier "com.4D.tool"'
)
# Windows x64: same 21.0 line. The version and signer are confirmed on a Windows runner before the build is listed
# as verified (VERIFIED_PLATFORMS in native.py).
WINDOWS_SOURCE_URL = "https://resources-download.4d.com/release/21.x/21.0/latest/win/tool4d_win.tar.xz"
WINDOWS_ARCHIVE_SHA256 = "41c23ea781d9711dbfca693d2aa90e0d8621eb2aa964a6510309435d4bd0b074"
WINDOWS_RUNTIME_VERSION = "21.0 build 21.100422"
WINDOWS_EXECUTABLE = Path("tool4d/tool4d.exe")
WINDOWS_SIGNER = "4D"  # required in the Authenticode signer certificate's subject (O= or CN=)
# Linux x86-64 (experimental, opt-in): same build as macOS and Windows. 4D ships no signature for it, so only the
# SHA-256 pin vouches for the archive. Needs glibc 2.38 or later (Ubuntu 24.04+).
LINUX_SOURCE_URL = "https://resources-download.4d.com/release/21.x/21.0/latest/linux/tool4d_linux.tar.xz"
LINUX_ARCHIVE_SHA256 = "5f2232d4f236483f0c9adefb15561eec01cb9d60b9985f8f9805d5087982fe7d"
LINUX_RUNTIME_VERSION = "21.0 build 21.100422"
LINUX_EXECUTABLE = Path("bin/tool4d")
# Shared libraries the Linux tool4d loads that Ubuntu 24.04 does not install by default (observed on ubuntu-latest).
LINUX_PACKAGES = ["libfreeimage3", "libc++1-18", "libc++abi1-18", "libunwind-18"]
MAX_ARCHIVE_BYTES = 64 * 1024 * 1024
MAX_EXPANDED_BYTES = 512 * 1024 * 1024


class ProvisionError(Exception):
    pass


def host(experimental: bool = False) -> str:
    """The provisioning platform: macos-arm64, windows-x64, or (experimental) linux-x64."""
    machine = platform.machine().lower()
    if sys.platform == "darwin" and machine == "arm64":
        return "macos-arm64"
    if sys.platform == "win32" and machine in {"amd64", "x86_64"}:
        return "windows-x64"
    if sys.platform.startswith("linux") and machine in {"x86_64", "amd64"}:
        if experimental:
            return "linux-x64"
        raise ProvisionError("Linux x86-64 provisioning is experimental; pass --experimental to opt in")
    raise ProvisionError("Automatic tool4d provisioning is verified only for macOS ARM64 and Windows x64")


def spec(name: str) -> dict:
    """Read at call time so tests can patch the module-level pins."""
    if name == "windows-x64":
        return {"url": WINDOWS_SOURCE_URL, "sha256": WINDOWS_ARCHIVE_SHA256, "version": WINDOWS_RUNTIME_VERSION,
                "executable": WINDOWS_EXECUTABLE, "root": "tool4d"}
    if name == "linux-x64":
        return {"url": LINUX_SOURCE_URL, "sha256": LINUX_ARCHIVE_SHA256, "version": LINUX_RUNTIME_VERSION,
                "executable": LINUX_EXECUTABLE, "root": "bin"}
    return {"url": SOURCE_URL, "sha256": ARCHIVE_SHA256, "version": RUNTIME_VERSION,
            "executable": EXECUTABLE, "root": "tool4d.app"}


def verify_archive(archive: Path, expected: str | None = None) -> None:
    expected = expected or ARCHIVE_SHA256
    if not 0 < archive.stat().st_size <= MAX_ARCHIVE_BYTES:
        raise ProvisionError("tool4d archive has an invalid size")
    digest = hashlib.sha256()
    with archive.open("rb") as file:
        for chunk in iter(lambda: file.read(1024 * 1024), b""):
            digest.update(chunk)
    if digest.hexdigest() != expected:
        raise ProvisionError(
            "tool4d archive SHA-256 does not match the reviewed pin; "
            "the upstream latest archive may have changed. No runtime was installed."
        )


def extract_archive(archive: Path, destination: Path, root: str = "tool4d.app") -> None:
    if not hasattr(tarfile, "data_filter"):
        raise ProvisionError("Safe extraction requires Python with tarfile data filters")
    with tarfile.open(archive, "r:xz") as source:
        members = source.getmembers()
        if not members or len(members) > 4096 or sum(m.size for m in members) > MAX_EXPANDED_BYTES:
            raise ProvisionError("tool4d archive exceeds the expected extraction bounds")
        for member in members:
            path = PurePosixPath(member.name)
            if not path.parts or path.is_absolute() or ".." in path.parts or path.parts[0] != root:
                raise ProvisionError("tool4d archive contains an unexpected path")
            if not (member.isfile() or member.isdir() or member.issym() or member.islnk()):
                raise ProvisionError("tool4d archive contains an unsupported member type")
        source.extractall(destination, members=members, filter="data")


def missing_libraries(tool: Path) -> list[str]:
    """Shared libraries the dynamic loader cannot find for `tool` (Linux only)."""
    completed = subprocess.run(["ldd", str(tool)], capture_output=True, text=True, timeout=30, check=False)
    return sorted({line.split("=>")[0].strip() for line in completed.stdout.splitlines() if "not found" in line})


def verify_signature(name: str, runtime: Path, tool: Path) -> str:
    if name == "linux-x64":
        missing = missing_libraries(tool)
        if missing:
            raise ProvisionError(
                f"tool4d needs shared libraries that are not installed: {', '.join(missing)}. "
                f"On Ubuntu 24.04: sudo apt-get install {' '.join(LINUX_PACKAGES)}")
        return "none: 4D ships no signature for Linux; only the SHA-256 pin vouches for the archive"
    if name == "windows-x64":
        script = ("$s = Get-AuthenticodeSignature -LiteralPath $env:SONAR4D_TOOL; "
                  "@{status = [string]$s.Status; subject = [string]$s.SignerCertificate.Subject} | ConvertTo-Json -Compress")
        completed = subprocess.run(["powershell", "-NoProfile", "-NonInteractive", "-Command", script],
                                   check=True, capture_output=True, text=True, timeout=60,
                                   env={**os.environ, "SONAR4D_TOOL": str(tool)})
        signature = json.loads(completed.stdout)
        subject = signature.get("subject") or ""
        names = {part.strip().split("=", 1)[1].strip('" ') for part in subject.split(",")
                 if part.strip().upper().startswith(("O=", "CN=")) and "=" in part}
        if signature.get("status") != "Valid" or not any(n == WINDOWS_SIGNER or n.startswith(WINDOWS_SIGNER + " ")
                                                         for n in names):
            raise ProvisionError(f"tool4d.exe Authenticode signature is not valid for {WINDOWS_SIGNER}: "
                                 f"status {signature.get('status')!r}, signer {subject!r}")
        return subject
    subprocess.run(
        ["codesign", "--verify", "--deep", "--strict", "--test-requirement",
         SIGNATURE_REQUIREMENT, str(runtime / "tool4d.app")],
        check=True, capture_output=True, text=True, timeout=30,
    )
    return SIGNATURE_REQUIREMENT


def install(destination: Path, archive: Path | None = None, experimental: bool = False) -> dict:
    name = host(experimental)
    pins = spec(name)
    destination = destination.resolve()
    if destination.exists():
        raise ProvisionError("tool4d destination already exists; choose a fresh directory")
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="sonar4d-install-", dir=destination.parent) as directory:
        staging = Path(directory).resolve()
        if archive is None:
            archive = staging / "tool4d.tar.xz"
            subprocess.run(
                ["curl", "--disable", "--fail", "--location", "--silent", "--show-error",
                 "--proto", "=https", "--proto-redir", "=https", "--max-time", "120",
                 "--max-filesize", str(MAX_ARCHIVE_BYTES), "--output", str(archive), pins["url"]],
                check=True, capture_output=True, text=True, timeout=125,
            )
        verify_archive(archive, pins["sha256"])
        runtime = staging / "runtime"
        extract_archive(archive, runtime, pins["root"])
        tool = runtime / pins["executable"]
        if not tool.is_file() or tool.is_symlink() or not os.access(tool, os.X_OK):
            raise ProvisionError("Archive does not contain the expected executable tool4d")
        signature = verify_signature(name, runtime, tool)
        version = tool4d.version(tool, 15)
        if version != pins["version"]:
            raise ProvisionError(f"Unexpected tool4d version: {version!r}")
        provenance = {
            "sourceUrl": pins["url"], "archiveSha256": pins["sha256"], "version": version,
            "platform": name, "signatureRequirement" if name == "macos-arm64" else "signer": signature,
            "toolPath": str(destination / pins["executable"]),
        }
        if name == "linux-x64":
            provenance["experimental"] = True
        (runtime / "provision.json").write_text(json.dumps(provenance, indent=2) + "\n", encoding="utf-8")
        if destination.exists():
            raise ProvisionError("tool4d destination was created concurrently; refusing replacement")
        runtime.rename(destination)
        return provenance


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Provision the pinned public tool4d runtime")
    parser.add_argument("--destination", type=Path, required=True, help="New installation directory")
    parser.add_argument("--archive", type=Path, help="Optional cached archive; the same digest pin applies")
    parser.add_argument("--experimental", action="store_true",
                        help="Allow experimental platforms (Linux x86-64): SHA-256 pin only, no signature check")
    args = parser.parse_args(argv)
    try:
        provenance = install(args.destination, args.archive, args.experimental)
    except (OSError, ValueError, tarfile.TarError, subprocess.SubprocessError, ProvisionError) as error:
        detail = error.stderr.strip() if isinstance(error, subprocess.CalledProcessError) and error.stderr else ""
        print(f"sonar-4d provision: {type(error).__name__}: {error}" + (f"\n{detail}" if detail else ""),
              file=sys.stderr)
        return 2
    print(json.dumps(provenance, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
