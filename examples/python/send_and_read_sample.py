#!/usr/bin/env python3
"""Minimal example: send a WhatsApp message and then read replies."""
from __future__ import annotations

from pathlib import Path

from python import WhatsAppBridge

# --- Configuration ---------------------------------------------------------
# Path to the compiled shared library produced by `make build` (adjust if needed).
LIB_PATH = Path(__file__).resolve().parent.parent / "dist" / "libwa.so"

if not LIB_PATH.exists():
    raise FileNotFoundError(
        "Не найден libwa.so. Сначала соберите проект командой `make build` или укажите путь вручную."
    )

# SQLite URI holding the WhatsApp session (created automatically on first run).
DB_URI = "file:./wa_bridge.db?_foreign_keys=on"

# Your own WhatsApp account phone number (used for the session / QR pairing).
ACCOUNT_PHONE = "79112100189"

# The chat to send to and read from.
CHAT_PHONE = "77771274847"

# --- Script logic ----------------------------------------------------------

def main() -> None:
    bridge = WhatsAppBridge(LIB_PATH)

    # 1) Ensure the library is connected. On the very first run this prints a QR.
    auth = bridge.run(DB_URI, ACCOUNT_PHONE, {"listen_seconds": 0, "show_qr": True})
    if auth.get("requires_qr"):
        print("Сканируйте QR-код, который появился в терминале с запуском Go-библиотеки.")

    # 2) Отправляем сообщение "Привет" в нужный чат.
    send_result = bridge.send_message(
        DB_URI,
        ACCOUNT_PHONE,
        CHAT_PHONE,
        "Привет",
        listen_seconds=0,
        show_qr=False,
    )
    print(f"Отправлено сообщение, id: {send_result.get('message_id', '<нет id>')}")

    # 3) Получаем последние 5 сообщений из того же чата и выводим их.
    read_result = bridge.read_messages(
        DB_URI,
        ACCOUNT_PHONE,
        CHAT_PHONE,
        read_limit=5,
        listen_seconds=0,
    )

    last_messages = read_result.get("last_messages") or []
    if not last_messages:
        print("Сообщений не найдено.")
        return

    print("Последние сообщения:")
    for idx, text in enumerate(last_messages[-5:], start=1):
        print(f"  {idx}) {text}")


if __name__ == "__main__":
    main()
