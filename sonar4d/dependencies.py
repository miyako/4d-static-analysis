"""Install declared 4D dependencies into the isolated workspace copy.

Resolution, download and verification are done by the 4D dependency manager emulation in miyako/4D
(`.github/actions/dependencies/fetch_dependencies.py`), fetched at a pinned commit and checked against a
recorded SHA-256. Alternatively (opt in, `manager="native"`) by the independent native `4d-components`
executable built from this repository's `components/` folder, which writes the same report and lock formats. tool4d's LSP mode does not load components from the dependency cache, so each resolved
package is copied into the copy's `Components/` folder. Existing `Components/` entries keep precedence.
"""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import urllib.request


SCRIPT_COMMIT = "f4aa7338a42fd239db4776e86b3061e309edc32e"
SCRIPT_SHA256 = "0b9324bce98dabaa6c4ba63425fc2350f32468dd4313f4d61ba13eb04824e1c1"
SCRIPT_URL = (
    f"https://raw.githubusercontent.com/miyako/4D/{SCRIPT_COMMIT}"
    "/.github/actions/dependencies/fetch_dependencies.py"
)
TOKEN_ENV = "SONAR4D_GITHUB_TOKEN"
MANAGERS = ("python", "native")
NATIVE_MANAGER_ENV = "SONAR4D_COMPONENTS"
LOCK_USER = "sonar4d"
MAX_SCRIPT_BYTES = 1024 * 1024
TIMEOUT = 600


class DependencyError(Exception):
    pass


def declared(project: Path) -> bool:
    """True when dependencies.json declares at least one dependency."""
    manifest_path = project.parent / "Sources/dependencies.json"
    if not manifest_path.is_file():
        return False
    manifest = json.loads(manifest_path.read_text(encoding="utf-8-sig"))
    if not isinstance(manifest, dict) or not isinstance(manifest.get("dependencies", {}), dict):
        raise DependencyError("Invalid 4D dependencies.json")
    return bool(manifest.get("dependencies"))


def carry_environment(package: Path, root: Path, workspace: Path) -> str | None:
    """Make the analysis copy use the environment4d.json that 4D would use for `package`.

    4D reads environment4d.json from the package folder or the nearest parent folder. A file at or below
    `root` is part of the copy already. A nearest file above `root` is copied to the copy's root (`workspace`);
    otherwise an empty one is written there, so the emulator never reaches an unrelated file above the
    temporary folder. Returns a warning when an outside file is applied.
    """
    target = workspace / "environment4d.json"
    if target.exists():
        return None
    for folder in [package, *package.parents]:
        candidate = folder / "environment4d.json"
        if candidate.is_file():
            if folder.is_relative_to(root):
                return None
            shutil.copyfile(candidate, target)
            return ("environment4d.json above the analysis root was applied to the analysis copy; "
                    "its relative paths resolve from the analysis root")
    target.write_text('{"dependencies": {}}\n', encoding="utf-8")
    return None


def cache_directory() -> Path:
    if sys.platform == "darwin":
        return Path.home() / "Library/Caches/sonar-4d"
    if os.name == "nt":
        return Path(os.environ.get("LOCALAPPDATA") or Path.home() / "AppData/Local") / "sonar-4d"
    return Path(os.environ.get("XDG_CACHE_HOME") or Path.home() / ".cache") / "sonar-4d"


def cache_paths() -> list[Path]:
    """Folders worth keeping between runs on ephemeral runners: sonar-4d's own cache (the pinned script and
    each project's last lock) and the emulator's verified archive store, which it keeps beside it in the
    same per-user cache folder. Both are re-verified on use, so a restored copy cannot install altered code."""
    return [cache_directory(), cache_directory().parent / "miyako-4d-dependencies" / "archives"]


def cache_settings(project: Path, tool_build: str) -> dict:
    """Inputs for the GitHub Action's dependency cache step."""
    return {
        "enabled": declared(project), "paths": [str(path) for path in cache_paths()],
        "prefix": f"sonar4d-dependencies-{SCRIPT_COMMIT[:12]}-{_sha256(tool_build.encode())[:12]}-",
    }


def _sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def script_path() -> Path:
    """Return the pinned emulator script, downloading it once; it is re-hashed on every use."""
    path = cache_directory() / "scripts" / f"fetch_dependencies-{SCRIPT_COMMIT[:12]}.py"
    if path.is_file() and _sha256(path.read_bytes()) == SCRIPT_SHA256:
        return path
    with urllib.request.urlopen(SCRIPT_URL, timeout=60) as response:
        data = response.read(MAX_SCRIPT_BYTES + 1)
    if len(data) > MAX_SCRIPT_BYTES or _sha256(data) != SCRIPT_SHA256:
        raise DependencyError("The dependency manager script does not match the reviewed pin")
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as temporary:
        temporary.write(data)
    os.replace(temporary.name, path)
    return path


def native_manager(path: str | os.PathLike | None = None) -> Path:
    """Return the native `4d-components` executable the caller provisioned.

    It is never downloaded here: the GitHub Action builds it from the reviewed source of this repository, and a
    local user passes its path (or sets SONAR4D_COMPONENTS). An unprovisioned manager is an error, never a
    silent fall back to the Python implementation."""
    candidate = path or os.environ.get(NATIVE_MANAGER_ENV)
    if not candidate:
        raise DependencyError("The native dependency manager is not provisioned: build components/cmd/4d-components "
                              f"and pass --dependency-manager-path (or set {NATIVE_MANAGER_ENV})")
    executable = Path(candidate).resolve()
    if not executable.is_file() or not os.access(executable, os.X_OK):
        raise DependencyError(f"The native dependency manager {str(candidate)!r} is not an executable file")
    return executable


def runtime_version(version: str) -> str:
    match = re.match(r"(\d+(?:R\d+)?(?:\.\d+)?)", version)
    if not match:
        raise DependencyError(f"Cannot read the tool4d version from {version!r}")
    return match[1]


def _local_source(entry: dict, package: Path, workspace: Path | None) -> tuple[Path | None, str | None]:
    """Return the folder to copy for a local component, or the reason it is not installed."""
    declared = repr(entry["local"]) if entry.get("local") else "with no path"
    if workspace is None:
        return None, "local dependencies are not installed by sonar-4d"
    if not entry.get("path"):
        return None, f"local component {declared} was not found or not resolved"
    folder = Path(entry["path"]).resolve()
    if not folder.is_relative_to(workspace.resolve()):
        return None, (f"local component {declared} is outside the analyzed repository; "
                      "sonar-4d only installs local components inside it")
    if not folder.is_dir():
        return None, f"local component {declared} was not found"
    if package.resolve().is_relative_to(folder):
        return None, f"local component {declared} contains the analyzed project"
    return folder, None


def install(package: Path, version: str, temporary: Path, *, offline: bool = False,
            token: str | None = None, script: Path | None = None,
            lock_key: str | None = None, workspace: Path | None = None,
            manager: str = "python", manager_path: str | os.PathLike | None = None) -> tuple[dict, str | None]:
    """Resolve and install dependencies for the workspace copy `package`.

    Local (`path`) components are installed only when they resolve inside `workspace`, the analysis copy of
    the repository; anything else stays incomplete. Without `workspace`, no local component is installed.

    Returns a summary for the observation and a blocker reason, or None when every dependency is installed.
    Like 4D, offline runs resolve version anchors from a lock file; sonar-4d keeps the last lock written
    for `lock_key` (the analyzed project) in its cache, because each analysis uses a fresh copy.
    """
    if manager not in MANAGERS:
        raise DependencyError(f"Unknown dependency manager {manager!r}; use one of {', '.join(MANAGERS)}")
    # Both managers write the same report and lock formats, so everything below is shared.
    if manager == "native":
        executable = [str(native_manager(manager_path)), "install"]
    else:
        executable = [sys.executable, str(script or script_path())]
    output = temporary / "dependencies-report.json"
    lock = package / f"userPreferences.{LOCK_USER}" / "dependencies-lock.json"
    saved = cache_directory() / "locks" / f"{_sha256(lock_key.encode())[:32]}.json" if lock_key else None
    if saved and saved.is_file() and not lock.exists():
        lock.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(saved, lock)
    command = [*executable, "--project", str(package), "--runtime", "tool4d",
               "--runtime-version", runtime_version(version), "--home", str(temporary / "home"),
               "--user", LOCK_USER, "--output", str(output)]
    if offline:
        command.append("--offline")
    environment = {key: value for key, value in os.environ.items()
                   if key not in {"GITHUB_OUTPUT", "DEPENDENCIES_GITHUB_TOKEN", "DEPENDENCIES_GITLAB_TOKEN"}}
    if token:
        environment["DEPENDENCIES_GITHUB_TOKEN"] = token
    completed = subprocess.run(command, env=environment, stdin=subprocess.DEVNULL, capture_output=True,
                               text=True, timeout=TIMEOUT)
    if completed.returncode not in {0, 3} or not output.is_file():
        detail = (completed.stderr.strip().splitlines() or [f"exit {completed.returncode}"])[-1]
        return {"token": bool(token)}, f"Dependency resolution failed: {detail}"
    report = json.loads(output.read_text(encoding="utf-8"))
    if saved and lock.is_file() and not report.get("incomplete"):
        saved.parent.mkdir(parents=True, exist_ok=True)
        staging = saved.with_suffix(f".{os.getpid()}.tmp")
        shutil.copyfile(lock, staging)
        os.replace(staging, saved)
    summary = {
        "resolved": report.get("resolved", {}), "token": bool(token), "offline": offline,
        "manager": manager, "archiveStore": report.get("archiveStore"), "warnings": report.get("warnings", []),
        "incomplete": report.get("incomplete", []), "installed": [], "providedByComponents": [],
        "reuse": {},
    }
    problems = [f"{entry['name']}: {entry['reason']}" for entry in summary["incomplete"]]
    components = package / "Components"
    for name, entry in sorted(report.get("dependencies", {}).items()):
        if entry.get("overloadedBy"):
            summary["providedByComponents"].append(name)
            continue
        if entry.get("source") == "local":
            if entry.get("error"):
                continue  # already reported by the emulator (unreadable manifest, version conflict)
            source, reason = _local_source(entry, package, workspace)
            if reason:
                problems.append(f"{name}: {reason}")
                continue
            target = components / f"{name}.4dbase"
            if target.exists():
                # As in 4D, a copy already in Components/ takes precedence.
                summary["providedByComponents"].append(name)
                continue
            components.mkdir(exist_ok=True)
            shutil.copytree(source, target)
            summary["installed"].append(name)
            summary["reuse"][name] = "local"
            continue
        source = entry.get("path")
        if not source or not Path(source).is_dir():
            if not any(problem.startswith(f"{name}:") for problem in problems):
                problems.append(f"{name}: not installed")
            continue
        components.mkdir(exist_ok=True)
        shutil.copytree(source, components / f"{name}.4dbase")
        summary["installed"].append(name)
        summary["reuse"][name] = entry.get("reuse")
    if problems:
        return summary, "Declared 4D dependencies are incomplete: " + "; ".join(problems)
    return summary, None
