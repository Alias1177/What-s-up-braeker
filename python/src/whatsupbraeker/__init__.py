"""High-level Python bindings for the WhatsUpBraeker Go bridge.

The package bundles the compiled ``libwa`` shared library and exposes a
minimal wrapper around its ``WaRun`` and ``WaFree`` functions.
"""

from __future__ import annotations

import ctypes
import json
import platform
from dataclasses import dataclass
from importlib import resources
from pathlib import Path
from typing import Any, Mapping, MutableMapping, Optional, Union

__all__ = [
    "LibraryLoadError",
    "WaBridge",
    "load_library",
    "run",
]

_LIB_EXTENSIONS = {
    "Darwin": ".dylib",
    "Linux": ".so",
    "Windows": ".dll",
}


class LibraryLoadError(RuntimeError):
    """Raised when the bundled shared library cannot be located or loaded."""


@dataclass
class WaResult:
    """Typed representation of the response JSON returned by the Go bridge."""

    status: str
    error: Optional[str]
    message_id: Optional[str]
    last_messages: list[str]
    requires_qr: bool

    @classmethod
    def from_mapping(cls, data: Mapping[str, Any]) -> "WaResult":
        """Create an instance from decoded JSON data."""
        return cls(
            status=str(data.get("status", "")),
            error=data.get("error"),
            message_id=data.get("message_id"),
            last_messages=list(data.get("last_messages") or []),
            requires_qr=bool(data.get("requires_qr", False)),
        )

    def to_dict(self) -> dict[str, Any]:
        """Return a plain dict that mirrors the original payload."""
        return {
            "status": self.status,
            "error": self.error,
            "message_id": self.message_id,
            "last_messages": list(self.last_messages),
            "requires_qr": self.requires_qr,
        }


class WaBridge:
    """Thin wrapper around the exported C functions."""

    def __init__(self, library: Optional[Union[str, Path]] = None) -> None:
        path = _resolve_library_path(library)
        self._lib = ctypes.CDLL(str(path))
        self._lib.WaRun.argtypes = [
            ctypes.c_char_p,
            ctypes.c_char_p,
            ctypes.c_char_p,
        ]
        self._lib.WaRun.restype = ctypes.c_char_p
        self._lib.WaFree.argtypes = [ctypes.c_char_p]
        self._lib.WaFree.restype = None

    def run(self, db_uri: str, account_phone: str, message: str) -> WaResult:
        """Call the Go bridge and return the structured response."""
        ptr = self._lib.WaRun(
            db_uri.encode("utf-8"),
            account_phone.encode("utf-8"),
            message.encode("utf-8"),
        )
        if not ptr:
            raise RuntimeError("WaRun returned NULL")
        try:
            raw = ctypes.string_at(ptr).decode("utf-8")
        finally:
            self._lib.WaFree(ptr)
        return WaResult.from_mapping(json.loads(raw))


def _resolve_library_path(candidate: Optional[Union[str, Path]]) -> Path:
    if candidate is not None:
        return Path(candidate).expanduser().resolve()

    library_name = _default_library_name()

    try:
        resource = resources.files(__name__).joinpath("lib", library_name)
    except AttributeError as exc:  # pragma: no cover - Python < 3.9 guard
        raise LibraryLoadError("importlib.resources API is unavailable") from exc

    if not resource.is_file():
        raise LibraryLoadError(
            f"Bundled shared library not found (expected: {resource})",
        )
    return Path(resource)


def load_library(path: Optional[Union[str, Path]] = None) -> WaBridge:
    """Return a ready-to-use ``WaBridge`` instance."""
    return WaBridge(path)


def run(
    db_uri: str,
    account_phone: str,
    message: str,
    *,
    library: Optional[Union[str, Path]] = None,
) -> MutableMapping[str, Any]:
    """Convenience helper mirroring the struct returned by the Go library."""
    result = load_library(library).run(db_uri, account_phone, message)
    return result.to_dict()


def _default_library_name() -> str:
    system = platform.system()
    try:
        extension = _LIB_EXTENSIONS[system]
    except KeyError as exc:  # pragma: no cover - defensive branch
        raise LibraryLoadError(f"Unsupported platform: {system!r}") from exc
    return f"libwa{extension}"
