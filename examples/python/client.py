#!/usr/bin/env python3
"""Example Python client for the Go shared library."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from python import BridgeError, WhatsAppBridge


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
        help="Text message to send (ignored with --read-only).",
    )
    parser.add_argument(
        "--read-only",
        action="store_true",
        help="Skip sending and only read incoming messages.",
    )
    parser.add_argument(
        "--read-limit",
        type=int,
        default=None,
        help="Maximum number of messages to collect (library default when omitted).",
    )
    parser.add_argument(
        "--listen-seconds",
        type=float,
        default=None,
        help="How long to listen for messages before returning.",
    )
    args = parser.parse_args(argv)

    lib_path = Path(args.lib)
    if not lib_path.is_absolute():
        lib_path = (Path(__file__).resolve().parent / lib_path).resolve()

    if not lib_path.exists():
        parser.error(f"shared library not found: {lib_path}")

    try:
        bridge = WhatsAppBridge(lib_path)
        payload = {}
        if not args.read_only:
            payload["send_text"] = args.message
        if args.read_limit is not None:
            payload["read_limit"] = args.read_limit
        if args.listen_seconds is not None:
            payload["listen_seconds"] = args.listen_seconds

        result = bridge.run(args.db_uri, args.phone, payload or None)
    except BridgeError as exc:  # pragma: no cover - defensive
        print(f"Bridge error: {exc}", file=sys.stderr)
        return 1
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

