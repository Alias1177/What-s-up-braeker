"""High-level Python bindings for the WhatsApp bridge shared library."""
from __future__ import annotations

import ctypes
import json
from pathlib import Path
from typing import Any, Dict, Optional

__all__ = ["BridgeError", "WhatsAppBridge"]


class BridgeError(RuntimeError):
    """Raised when the Go bridge reports an error status."""


class WhatsAppBridge:
    """Thin OO wrapper around the ``libwa`` shared library."""

    def __init__(self, lib_path: str | Path, *, raise_on_error: bool = True) -> None:
        path = Path(lib_path)
        if not path.exists():
            raise FileNotFoundError(f"shared library not found: {path}")

        self._lib = ctypes.CDLL(str(path))
        self._lib.WaRun.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_char_p]
        self._lib.WaRun.restype = ctypes.c_char_p
        self._lib.WaFree.argtypes = [ctypes.c_char_p]
        self._lib.WaFree.restype = None
        self._raise_on_error = raise_on_error

    def run(
        self,
        db_uri: str,
        account_phone: str,
        payload: Optional[Dict[str, Any] | str] = None,
    ) -> Dict[str, Any]:
        """Invoke the shared library with an optional JSON payload."""

        message: str
        if payload is None:
            message = ""
        elif isinstance(payload, str):
            message = payload
        else:
            message = json.dumps(payload, ensure_ascii=False)

        ptr = self._lib.WaRun(
            db_uri.encode("utf-8"),
            account_phone.encode("utf-8"),
            message.encode("utf-8"),
        )
        if not ptr:
            raise RuntimeError("library returned NULL pointer")

        try:
            raw = ctypes.string_at(ptr).decode("utf-8")
        finally:
            self._lib.WaFree(ptr)

        result: Dict[str, Any] = json.loads(raw)
        if self._raise_on_error and result.get("status") != "ok":
            raise BridgeError(result.get("error", "unknown bridge error"))
        return result

    def send_message(
        self,
        db_uri: str,
        account_phone: str,
        recipient: str,
        text: str,
        *,
        read_chat: Optional[str] = None,
        read_limit: Optional[int] = None,
        listen_seconds: Optional[float] = None,
        show_qr: bool = False,
        force_relink: bool = False,
    ) -> Dict[str, Any]:
        """Send ``text`` to ``recipient`` and optionally listen for replies."""

        payload: Dict[str, Any] = {
            "send_text": text,
            "recipient": recipient,
        }
        if read_chat:
            payload["read_chat"] = read_chat
        else:
            payload["read_chat"] = recipient
        if read_limit is not None:
            payload["read_limit"] = read_limit
        if listen_seconds is not None:
            payload["listen_seconds"] = listen_seconds
        if show_qr:
            payload["show_qr"] = True
        if force_relink:
            payload["force_relink"] = True
        return self.run(db_uri, account_phone, payload)

    def read_messages(
        self,
        db_uri: str,
        account_phone: str,
        chat: str,
        *,
        read_limit: Optional[int] = None,
        listen_seconds: Optional[float] = None,
        show_qr: bool = False,
        force_relink: bool = False,
    ) -> Dict[str, Any]:
        """Listen to incoming messages from ``chat`` without sending anything."""

        payload: Dict[str, Any] = {"read_chat": chat}
        if read_limit is not None:
            payload["read_limit"] = read_limit
        if listen_seconds is not None:
            payload["listen_seconds"] = listen_seconds
        if show_qr:
            payload["show_qr"] = True
        if force_relink:
            payload["force_relink"] = True
        return self.run(db_uri, account_phone, payload)

