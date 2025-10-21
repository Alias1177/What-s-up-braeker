#!/usr/bin/env python3
"""Standalone Python script that calls the WhatsApp bridge shared library."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from python import BridgeError, WhatsAppBridge


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
        "--account-phone",
        required=True,
        help="WhatsApp account phone (used for the session / QR pairing).",
    )
    parser.add_argument(
        "--recipient",
        help="Phone or JID of the chat to send to.",
    )
    parser.add_argument(
        "--message",
        default="Hello from the standalone Python client!",
        help="Text message to send (ignored with --read-only).",
    )
    parser.add_argument(
        "--read-only",
        action="store_true",
        help="Skip sending a message and only collect incoming messages.",
    )
    parser.add_argument(
        "--read-limit",
        type=int,
        default=None,
        help="Maximum number of messages to collect (default is library-controlled).",
    )
    parser.add_argument(
        "--listen-seconds",
        type=float,
        default=None,
        help="How long to listen for messages (fractional seconds allowed).",
    )
    parser.add_argument(
        "--read-only",
        action="store_true",
        help="Skip sending a message and only collect incoming messages.",
    )
    parser.add_argument(
        "--read-limit",
        type=int,
        default=None,
        help="Maximum number of messages to collect (default is library-controlled).",
    )
    parser.add_argument(
        "--listen-seconds",
        type=float,
        default=None,
        help="How long to listen for messages (fractional seconds allowed).",
    )
    parser.add_argument(
        "--read-chat",
        default=None,
        help="Phone or JID of the chat to collect messages from (defaults to recipient).",
    )
    parser.add_argument(
        "--show-qr",
        action="store_true",
        help="Print QR codes when login is required.",
    )
    parser.add_argument(
        "--force-relink",
        action="store_true",
        help="Force the stored session to be cleared and request a new QR link.",
    )
    args = parser.parse_args(argv)
    return parser, args


def main(argv: list[str] | None = None) -> int:
    parser, args = parse_args(argv)

    lib_path = Path(args.lib)
    if not lib_path.is_absolute():
        lib_path = (Path(__file__).resolve().parent / lib_path).resolve()

    try:
        bridge = WhatsAppBridge(lib_path)
        payload = {}
        if not args.read_only:
            payload["send_text"] = args.message
        if args.read_limit is not None:
            payload["read_limit"] = args.read_limit
        if args.listen_seconds is not None:
            payload["listen_seconds"] = args.listen_seconds

        request_payload = payload or None
        result = bridge.run(args.db_uri, args.phone, request_payload)
    except BridgeError as exc:  # pragma: no cover
        print(f"Bridge error: {exc}", file=sys.stderr)
        return 1
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

