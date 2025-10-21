# What-s-up-braeker

Go-библиотека для работы с [WhatsApp](https://www.whatsapp.com/) через [`whatsmeow`](https://github.com/tulir/whatsmeow). Пакет предоставляет минимальный API, пригодный для собственных Go-приложений и для использования через `ctypes` в Python.

## Структура проекта
- `pkg/waclient` — основная логика соединения, отправки и получения сообщений.
- `cmd/wa-bridge` — точка сборки `c-shared`, экспортирующая функции `WaRun` и `WaFree`.
- `examples/python/client.py` — пример вызова общей библиотеки из Python.
- `main.go` — демонстрация использования пакета напрямую из Go.

## Использование в Go
```bash
go run ./main.go
```
Перед запуском задайте номер телефона и текст сообщения в `main.go`. При первой авторизации появится QR-код, который нужно отсканировать в WhatsApp.

Чтобы подключить библиотеку в другом проекте:
```go
import "github.com/Alias1177/What-s-up-braeker/pkg/waclient"
```

## Сборка динамической библиотеки
```bash
mkdir -p dist
go build -buildmode=c-shared -o dist/libwa.so ./cmd/wa-bridge
```
Команда создаст `dist/libwa.so` и заголовок `dist/libwa.h`. В средах с ограниченными правами на запись в глобальные кэши можно использовать локальные каталоги:
```bash
GOTOOLCHAIN=go1.24.0 \
GOMODCACHE=$PWD/.gomodcache \
GOCACHE=$PWD/.gocache \
go build -buildmode=c-shared -o dist/libwa.so ./cmd/wa-bridge
```

## Пример на Python
После сборки библиотеки:
```bash
python3 examples/python/client.py \
  --account-phone 79991234567 \
  --recipient 79990001122 \
  --message "Hello from Python!" \
  --read-limit 5 \
  --listen-seconds 8 \
  --show-qr
```
Флаги `--read-limit` и `--listen-seconds` позволяют регулировать, сколько входящих сообщений будет собрано и как долго ждать ответов. Передайте `--read-only`, чтобы ничего не отправлять и просто прочитать чат. Флаг `--lib` позволяет указать путь к `.so`, `--db-uri` — строку подключения к SQLite с сохранённой сессией WhatsApp, `--show-qr` заставит бридж печатать QR-код, когда требуется авторизация, а `--force-relink` очищает текущую сессию и инициирует новую привязку по QR.

### Использование в собственном скрипте Python

```python
from pathlib import Path

from python import WhatsAppBridge

bridge = WhatsAppBridge(Path("dist/libwa.so"))

# Отправить сообщение и подождать до 3 ответов не дольше 15 секунд
response = bridge.send_message(
    db_uri="file:whatsapp.db?_foreign_keys=on",
    account_phone="79991234567",
    recipient="79990001122",
    text="Привет!",
    read_limit=3,
    listen_seconds=15,
)

# Только прочитать сообщения — будет использован JSON-пейлоад без текста
history = bridge.read_messages(
    db_uri="file:whatsapp.db?_foreign_keys=on",
    account_phone="79991234567",
    chat="79990001122",
    read_limit=10,
)
```
Под капотом `WhatsAppBridge` формирует JSON-параметры для функции `WaRun`. Доступные поля:

- `send_text` — текст сообщения, который нужно отправить (если опущен, работает режим чтения);
- `recipient` — номер или JID чата, куда нужно отправить сообщение;
- `read_chat` — номер или JID чата, из которого следует читать сообщения (по умолчанию совпадает с `recipient`);
- `read_limit` — сколько входящих сообщений вернуть (по умолчанию библиотека берёт разумное значение);
- `listen_seconds` — максимальное время ожидания новых сообщений (дробное число секунд);
- `show_qr` — печатать ли QR-коды в консоли, когда требуется авторизация (существующая сессия повторно не сбрасывается);
- `force_relink` — очищает сохранённую сессию и заставляет библиотеку запросить новый QR для указанного `account_phone`.

## Требования
- Go 1.24+ (для зависимостей `whatsmeow`);
- база WhatsApp (`whatsapp.db`) рядом с бинарём;
- Python 3.8+ (для примера).

## Примечания
- В `pkg/waclient.Config` можно управлять тайм-аутами, логами и выводом QR.
- `WaRun` возвращает JSON со статусом, ID отправленного сообщения, собранными в сессии сообщениями и флагом `requires_qr`.
- При необходимости можно передать JSON-строку напрямую в `message` (например, `{"send_text":"hi","read_limit":5}`).
