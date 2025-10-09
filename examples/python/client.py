#!/usr/bin/env python3
"""
Example Python client for the Go shared library.
"""

from __future__ import annotations

import argparse
import ctypes
import json
import sys
from pathlib import Path


def load_library(path: Path) -> ctypes.CDLL:
    lib = ctypes.CDLL(str(path))
    lib.WaRun.argtypes = [ctypes.c_char_p, ctypes.c_char_p, ctypes.c_char_p]
    lib.WaRun.restype = ctypes.c_char_p
    lib.WaFree.argtypes = [ctypes.c_char_p]
    lib.WaFree.restype = None
    return lib


def call_run(lib: ctypes.CDLL, db_uri: str, phone: str, message: str) -> dict:
    ptr = lib.WaRun(
        db_uri.encode("utf-8"),
        phone.encode("utf-8"),
        message.encode("utf-8"),
    )
    if not ptr:
        raise RuntimeError("library returned NULL pointer")

    try:
        raw = ctypes.string_at(ptr).decode("utf-8")
    finally:
        lib.WaFree(ptr)

    return json.loads(raw)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Interact with the Go WhatsApp bridge library.")
    parser.add_argument(
        "--lib",
        default="../../dist/libwa.so",
        help="Path to the compiled shared library (default: ../../dist/libwa.so).",
    )
    parser.add_argument(
        "--db-uri",
        default="file:whatsapp.db?_foreign_keys=on",
        help="SQLite connection string with WhatsApp session data.",
    )
    parser.add_argument(
        "--phone",
        required=True,
        help="Recipient phone in the international format without '+'.",
    )
    parser.add_argument(
        "--message",
        default="Hello from Python!",
        help="Text message to send.",
    )
    args = parser.parse_args(argv)

    lib_path = Path(args.lib)
    if not lib_path.is_absolute():
        lib_path = (Path(__file__).resolve().parent / lib_path).resolve()

    if not lib_path.exists():
        parser.error(f"shared library not found: {lib_path}")

    try:
        lib = load_library(lib_path)
        result = call_run(lib, args.db_uri, args.phone, args.message)
    except Exception as exc:  # pragma: no cover - defensive
        print(f"Error calling library: {exc}", file=sys.stderr)
        return 1

    status = result.get("status")
    if status != "ok":
        print(f"Library reported error: {result.get('error', 'unknown error')}", file=sys.stderr)
        return 1

    print("Library call succeeded.")
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

