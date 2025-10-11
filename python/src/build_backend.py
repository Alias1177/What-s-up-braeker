from __future__ import annotations

import os
import platform
import shutil
import subprocess
from pathlib import Path
from typing import Optional

from distutils.util import get_platform
from setuptools import Command
from setuptools.command.build_py import build_py as build_py_orig
from wheel.bdist_wheel import bdist_wheel as bdist_wheel_orig


_LIB_EXTENSIONS = {
    "Darwin": ".dylib",
    "Linux": ".so",
    "Windows": ".dll",
}


class build_go(Command):
    """Build the Go shared library bundled with the package."""

    description = "build the Go shared library"
    user_options: list[tuple[str, Optional[str], str]] = []

    def initialize_options(self) -> None:
        self.build_lib: Optional[Path] = None
        self.build_temp: Optional[Path] = None
        self.output_path: Optional[Path] = None

    def finalize_options(self) -> None:
        build_py = self.get_finalized_command("build_py")
        self.build_lib = Path(build_py.build_lib)

        build_cmd = self.get_finalized_command("build")
        self.build_temp = Path(build_cmd.build_temp)

    def run(self) -> None:
        system = platform.system()
        try:
            extension = _LIB_EXTENSIONS[system]
        except KeyError as exc:  # pragma: no cover - safety guard
            raise RuntimeError(f"Unsupported platform: {system!r}") from exc

        project_root = Path(__file__).resolve().parents[2]
        build_dir = self.build_temp or (project_root / "build")
        build_dir.mkdir(parents=True, exist_ok=True)

        output_name = f"libwa{extension}"
        artifact = build_dir / output_name

        env = os.environ.copy()
        env.setdefault("CGO_ENABLED", "1")

        command = [
            "go",
            "build",
            "-buildmode=c-shared",
            "-o",
            str(artifact),
            "./cmd/wa-bridge",
        ]

        self.announce(f"building Go shared library via: {' '.join(command)}", level=2)
        subprocess.run(command, check=True, cwd=project_root, env=env)

        package_lib_dir = (self.build_lib or project_root) / "whatsupbraeker" / "lib"
        package_lib_dir.mkdir(parents=True, exist_ok=True)

        destination = package_lib_dir / output_name
        shutil.copy2(artifact, destination)

        self.output_path = destination


class build_py(build_py_orig):
    """Ensure the Go library is compiled as part of the Python build."""

    def run(self) -> None:
        super().run()
        self.run_command("build_go")

    def get_outputs(self, include_bytecode: bool = True):
        outputs = list(super().get_outputs(include_bytecode=include_bytecode))
        go_cmd: build_go = self.get_finalized_command("build_go")
        if getattr(go_cmd, "output_path", None):
            outputs.append(str(go_cmd.output_path))
        return outputs


class bdist_wheel(bdist_wheel_orig):
    """Mark the wheel as platform specific."""

    def finalize_options(self) -> None:
        super().finalize_options()
        self.root_is_pure = False
        if not self.plat_name:
            self.plat_name = get_platform().replace("-", "_")
