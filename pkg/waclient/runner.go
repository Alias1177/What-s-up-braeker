package waclient

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// Config contains parameters used to run the WhatsApp client.
type Config struct {
	DatabaseURI       string
	PhoneNumber       string
	Message           string
	WaitBeforeSend    time.Duration
	ListenAfterSend   time.Duration
	Output            io.Writer
	QRWriter          io.Writer
	LogLevel          string
	LogEnableColor    bool
	DisableQRPrinting bool
}

// Result holds the outcome of running the WhatsApp client.
type Result struct {
	LastMessages []string
	MessageID    string
	RequiresQR   bool
}

// Run spins up the WhatsApp client, optionally shows QR code, sends a message and collects session messages.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if cfg.DatabaseURI == "" {
		cfg.DatabaseURI = "file:whatsapp.db?_foreign_keys=on"
	}

	out := cfg.Output
	if out == nil {
		out = os.Stdout
	}

	qrOut := cfg.QRWriter
	if qrOut == nil {
		qrOut = out
	}

	waitBeforeSend := cfg.WaitBeforeSend
	if waitBeforeSend <= 0 {
		waitBeforeSend = 5 * time.Second
	}

	listenAfterSend := cfg.ListenAfterSend
	if listenAfterSend <= 0 {
		listenAfterSend = 10 * time.Second
	}

	logLevel := cfg.LogLevel
	if logLevel == "" {
		logLevel = "INFO"
	}

	log := waLog.Stdout("Client", logLevel, cfg.LogEnableColor)

	container, err := sqlstore.New(ctx, "sqlite3", cfg.DatabaseURI, log)
	if err != nil {
		return nil, fmt.Errorf("init store: %w", err)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("get device: %w", err)
	}

	client := whatsmeow.NewClient(deviceStore, log)

	var (
		messagesMu   sync.Mutex
		lastMessages []string
	)
	println := func(format string, args ...interface{}) {
		messagesMu.Lock()
		defer messagesMu.Unlock()
		fmt.Fprintf(out, format+"\n", args...)
	}

	client.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			sender := "Собеседник"
			if v.Info.IsFromMe {
				sender = "Ты"
			}

			text := v.Message.GetConversation()
			if text == "" && v.Message.ExtendedTextMessage != nil {
				text = v.Message.ExtendedTextMessage.GetText()
			}

			if text != "" {
				timestamp := v.Info.Timestamp
				msg := fmt.Sprintf("[%s] %s: %s",
					timestamp.Format("02.01.2006 15:04"),
					sender,
					text,
				)
				messagesMu.Lock()
				lastMessages = append(lastMessages, msg)
				messagesMu.Unlock()
				println("📩 Новое сообщение: %s", msg)
			}

		case *events.HistorySync:
			println("📚 Получена история чатов")
		}
	})

	result := &Result{}
	if client.Store.ID == nil {
		result.RequiresQR = true
		println("Отсканируй QR-код в WhatsApp:")

		qrChan, _ := client.GetQRChannel(context.Background())
		if err = client.Connect(); err != nil {
			return nil, fmt.Errorf("connect (qr): %w", err)
		}
		for evt := range qrChan {
			if evt.Event == "code" && !cfg.DisableQRPrinting {
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, qrOut)
			} else {
				println("Событие: %s", evt.Event)
			}
		}
	} else {
		if err = client.Connect(); err != nil {
			return nil, fmt.Errorf("connect: %w", err)
		}
		println("✅ Подключено к WhatsApp!")
	}

	connected := true
	defer func() {
		if connected {
			client.Disconnect()
		}
	}()

	println("Жду стабилизации соединения...")
	time.Sleep(waitBeforeSend)

	if cfg.PhoneNumber == "" {
		return result, fmt.Errorf("phone number is required")
	}

	recipientJID := types.NewJID(cfg.PhoneNumber, types.DefaultUserServer)

	println("\n📥 Последние сообщения за текущий запуск...")
	messagesMu.Lock()
	if len(lastMessages) > 0 {
		fmt.Fprintln(out, "\n--- Последние сообщения ---")
		for i, msg := range lastMessages {
			fmt.Fprintf(out, "\n%d) %s\n", i+1, msg)
		}
		fmt.Fprintln(out, "---------------------------\n")
	} else {
		fmt.Fprintln(out, "⚠️ Пока нет полученных сообщений в этой сессии")
	}
	messagesMu.Unlock()

	println("📤 Отправляю сообщение...")
	resp, err := client.SendMessage(context.Background(), recipientJID, &waProto.Message{
		Conversation: proto.String(cfg.Message),
	})
	if err != nil {
		return result, fmt.Errorf("send message: %w", err)
	}
	result.MessageID = resp.ID
	println("✅ Сообщение отправлено! ID: %s", resp.ID)

	println("\n👂 Слушаю новые сообщения %d секунд...", int(listenAfterSend.Seconds()))
	time.Sleep(listenAfterSend)

	messagesMu.Lock()
	result.LastMessages = append(result.LastMessages, lastMessages...)
	messagesMu.Unlock()

	println("\nОтключаюсь...")
	connected = false
	client.Disconnect()
	return result, nil
}
