#!/usr/bin/env python3
"""
Standalone Python script that calls the WhatsApp bridge shared library.
"""

from __future__ import annotations

import argparse
import ctypes
import json
import sys
from pathlib import Path


class WaBridge:
    """Thin wrapper around the `libwa.so` exported functions."""

    def __init__(self, lib_path: Path) -> None:
        if not lib_path.exists():
            raise FileNotFoundError(f"shared library not found: {lib_path}")

        self._lib = ctypes.CDLL(str(lib_path))
        self._lib.WaRun.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_char_p]
        self._lib.WaRun.restype = ctypes.c_char_p
        self._lib.WaFree.argtypes = [ctypes.c_char_p]
        self._lib.WaFree.restype = None

    def run(self, db_uri: str, phone: str, message: str) -> dict:
        """Invoke the Go bridge and return decoded JSON data."""
        ptr = self._lib.WaRun(
            db_uri.encode("utf-8"),
            phone.encode("utf-8"),
            message.encode("utf-8"),
        )
        if not ptr:
            raise RuntimeError("library returned NULL pointer")

        try:
            raw = ctypes.string_at(ptr).decode("utf-8")
        finally:
            self._lib.WaFree(ptr)

        return json.loads(raw)


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Send a WhatsApp message via the Go bridge library.",
    )
    parser.add_argument(
        "--lib",
        default="../dist/libwa.so",
        help="Path to the compiled libwa shared library (default: ../dist/libwa.so).",
    )
    parser.add_argument(
        "--db-uri",
        default="file:whatsapp.db?_foreign_keys=on",
        help="SQLite connection string with WhatsApp session data.",
    )
    parser.add_argument(
        "--phone",
        required=True,
        help="Recipient phone in international format without '+'.",
    )
    parser.add_argument(
        "--message",
        default="Hello from the standalone Python client!",
        help="Text message to send.",
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)

    lib_path = Path(args.lib)
    if not lib_path.is_absolute():
        lib_path = (Path(__file__).resolve().parent / lib_path).resolve()

    try:
        bridge = WaBridge(lib_path)
        result = bridge.run(args.db_uri, args.phone, args.message)
    except Exception as exc:  # pragma: no cover
        print(f"Error: {exc}", file=sys.stderr)
        return 1

    status = result.get("status")
    if status != "ok":
        print(f"Bridge error: {result.get('error', 'unknown error')}", file=sys.stderr)
        return 1

    print("WhatsApp bridge call succeeded.")
    print(f"- Message ID: {result.get('message_id', '<none>')}")
    print(f"- Login required: {'yes' if result.get('requires_qr') else 'no'}")

    last_messages = result.get("last_messages") or []
    if last_messages:
        print("- Session messages:")
        for idx, msg in enumerate(last_messages, start=1):
            print(f"  {idx}) {msg}")

    return 0


if __name__ == "__main__":
    sys.exit(main())

