"""The opt-in native `4d-components` dependency manager as the analyzer uses it."""

import contextlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from sonar4d import dependencies
from sonar4d.cli import main

COMPONENTS = Path(__file__).resolve().parents[1] / "components"


def build_native(directory: Path) -> Path | None:
    go = shutil.which("go")
    if not go or not COMPONENTS.is_dir():
        return None
    executable = directory / ("4d-components.exe" if os.name == "nt" else "4d-components")
    env = {**os.environ, "CGO_ENABLED": "0", "GOTOOLCHAIN": "local", "GOFLAGS": "-mod=readonly"}
    done = subprocess.run([go, "build", "-trimpath", "-o", str(executable), "./cmd/4d-components"],
                          cwd=COMPONENTS, env=env, capture_output=True, text=True, timeout=600)
    return executable if done.returncode == 0 else None


class NativeManagerContract(unittest.TestCase):
    def setUp(self):
        self.directory = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.directory, True)
        self.package = self.directory / "package"
        self.package.mkdir()
        self.executable = self.directory / "4d-components"
        self.executable.write_text("#!/bin/sh\n")
        self.executable.chmod(0o755)
        cache = patch("sonar4d.dependencies.cache_directory", return_value=self.directory / "sonar4d-cache")
        cache.start()
        self.addCleanup(cache.stop)

    def run_install(self, report, **options):
        calls = []

        def fake_run(command, **kwargs):
            calls.append((command, kwargs))
            Path(command[command.index("--output") + 1]).write_text(json.dumps(report), encoding="utf-8")
            return subprocess.CompletedProcess(command, 0, "", "")

        with patch("sonar4d.dependencies.subprocess.run", fake_run):
            result = dependencies.install(self.package, "21.0 build 21.100422", self.directory,
                                          manager="native", manager_path=self.executable, **options)
        return calls, result

    def test_invokes_the_install_command_with_the_shared_contract_and_keeps_the_token_out_of_argv(self):
        cached = self.directory / "cache/A"
        (cached / "Project").mkdir(parents=True)
        (cached / "Project/A.4DProject").write_text("{}")
        calls, (summary, blocker) = self.run_install(
            {"resolved": {"A": "1.0.0"}, "incomplete": [], "dependencies": {
                "A": {"source": "github", "path": str(cached), "reuse": "verified archive"}}},
            token="secret-token", offline=True)
        self.assertIsNone(blocker)
        command, kwargs = calls[0]
        self.assertEqual(command[:2], [str(self.executable.resolve()), "install"])
        self.assertEqual(command[command.index("--runtime-version") + 1], "21.0")
        self.assertEqual(command[command.index("--user") + 1], dependencies.LOCK_USER)
        self.assertIn("--offline", command)
        self.assertNotIn("secret-token", " ".join(command))
        self.assertEqual(kwargs["env"]["DEPENDENCIES_GITHUB_TOKEN"], "secret-token")
        self.assertNotIn("DEPENDENCIES_GITLAB_TOKEN", kwargs["env"])
        self.assertEqual(summary["manager"], "native")
        self.assertEqual(summary["installed"], ["A"])
        self.assertTrue((self.package / "Components/A.4dbase/Project/A.4DProject").is_file())
        self.assertNotIn("secret-token", json.dumps(summary))

    def test_an_incomplete_report_is_still_a_blocker(self):
        _, (summary, blocker) = self.run_install(
            {"incomplete": [{"name": "A", "reason": "no release"}], "dependencies": {}})
        self.assertIn("A: no release", blocker)

    def test_a_missing_or_unprovisioned_manager_is_an_error_not_a_fallback(self):
        with patch.dict(os.environ, {}, clear=False):
            os.environ.pop(dependencies.NATIVE_MANAGER_ENV, None)
            with self.assertRaises(dependencies.DependencyError):
                dependencies.native_manager(None)
        with self.assertRaises(dependencies.DependencyError):
            dependencies.native_manager(self.directory / "missing")
        with self.assertRaises(dependencies.DependencyError):
            dependencies.install(self.package, "21.0", self.directory, manager="nope")

    def test_cli_validates_the_manager_options(self):
        root = self.directory / "repo"
        root.mkdir()
        base = [str(root), "--output", str(self.directory / "out")]
        for extra in (["--dependency-manager", "native"],
                      ["--dependency-manager-path", str(self.executable)],
                      ["--dependency-manager", "bogus"]):
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) if extra[1] == "bogus" \
                    else contextlib.nullcontext():
                status = main(base + extra)
                if extra[1] != "bogus":
                    self.assertEqual(status, 2)
        with contextlib.redirect_stderr(io.StringIO()), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(main(base + ["--native-tool", str(self.executable), "--project", "P.4DProject",
                                          "--dependency-manager-path", str(self.executable)]), 2)


@unittest.skipUnless(shutil.which("go"), "the Go toolchain is not available")
class NativeManagerEndToEnd(unittest.TestCase):
    """Runs the real executable with local components only, so no network is needed."""

    @classmethod
    def setUpClass(cls):
        cls.directory = Path(tempfile.mkdtemp())
        cls.executable = build_native(cls.directory)

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.directory, True)

    def setUp(self):
        if self.executable is None:
            self.skipTest("the native manager could not be built")
        self.work = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.work, True)
        cache = patch("sonar4d.dependencies.cache_directory", return_value=self.work / "sonar4d-cache")
        cache.start()
        self.addCleanup(cache.stop)

    def make_workspace(self):
        workspace = self.work / "workspace"
        package = workspace / "Probe"
        (package / "Project/Sources").mkdir(parents=True)
        (package / "Project/Probe.4DProject").write_text("{}")
        (package / "Project/Sources/dependencies.json").write_text(
            '{"dependencies": {"Loc": {"path": "Loc"}, "Missing": {"path": "Nowhere"}}}')
        (workspace / "Loc/Project").mkdir(parents=True)
        (workspace / "Loc/Project/Loc.4DProject").write_text("{}")
        return workspace, package

    def test_local_components_inside_the_workspace_are_installed_and_missing_ones_block(self):
        workspace, package = self.make_workspace()
        summary, blocker = dependencies.install(package, "21.0 build 21.100422", self.work, workspace=workspace,
                                                manager="native", manager_path=self.executable)
        self.assertEqual(summary["installed"], ["Loc"])
        self.assertTrue((package / "Components/Loc.4dbase/Project/Loc.4DProject").is_file())
        self.assertIn("Missing", blocker)
        self.assertEqual(summary["manager"], "native")

    def test_python_and_native_install_the_same_local_layout(self):
        reference = Path("/home/user/miyako/4D/.github/actions/dependencies/fetch_dependencies.py")
        reference = Path(os.environ.get("REF_SCRIPT", reference))
        if not reference.is_file():
            self.skipTest("the reference script is not available")
        results = {}
        for manager in ("python", "native"):
            workspace, package = self.make_workspace()
            shutil.move(str(workspace), str(self.work / f"workspace-{manager}"))
            workspace = self.work / f"workspace-{manager}"
            package = workspace / "Probe"
            with patch.object(dependencies, "script_path", return_value=reference):
                summary, blocker = dependencies.install(
                    package, "21.0 build 21.100422", self.work, workspace=workspace, manager=manager,
                    manager_path=self.executable)
            results[manager] = (summary["installed"], summary["providedByComponents"], blocker,
                                sorted(p.relative_to(package).as_posix() for p in (package / "Components").rglob("*")))
        self.assertEqual(results["python"], results["native"])


if __name__ == "__main__":
    unittest.main()
