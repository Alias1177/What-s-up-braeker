package waclient

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
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
	Chat              string
	Message           string
	WaitBeforeSend    time.Duration
	ListenAfterSend   time.Duration
	ReadLimit         int
	Output            io.Writer
	QRWriter          io.Writer
	LogLevel          string
	LogEnableColor    bool
	DisableQRPrinting bool
	IncludeFromMe     bool
	IncludeFromMeSet  bool
}

// Result holds the outcome of running the WhatsApp client.
type Result struct {
	LastMessages []string
	MessageID    string
	RequiresQR   bool
}

// Run spins up the WhatsApp client, optionally shows QR code, sends a message and collects session messages.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if strings.TrimSpace(cfg.PhoneNumber) == "" && strings.TrimSpace(cfg.Chat) == "" {
		return nil, fmt.Errorf("phone number or chat is required")
	}

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

	readLimit := cfg.ReadLimit
	if readLimit < 0 {
		readLimit = 0
	}

	logLevel := cfg.LogLevel
	if logLevel == "" {
		logLevel = "INFO"
	}

	targetJID, targetJIDString, err := resolveTargetJID(cfg)
	if err != nil {
		return nil, err
	}

	if cfg.Message != "" && targetJIDString == "" {
		return nil, fmt.Errorf("target chat is required to send messages")
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
		messageLog   []messageRecord
		seenMessages = make(map[string]struct{})
	)
	println := func(format string, args ...interface{}) {
		messagesMu.Lock()
		defer messagesMu.Unlock()
		fmt.Fprintf(out, format+"\n", args...)
	}

	includeFromMe := cfg.IncludeFromMe
	if !cfg.IncludeFromMeSet {
		includeFromMe = true
	}

	client.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			if targetJIDString != "" && v.Info.Chat.String() != targetJIDString {
				return
			}
			if v.Info.IsFromMe && !includeFromMe {
				return
			}

			if record, ok := newMessageRecord(v); ok {
				if appendRecord(&messagesMu, &messageLog, seenMessages, record) {
					println("📩 Новое сообщение: %s", record.content)
				}
			}

		case *events.HistorySync:
			added := processHistorySyncMessages(client, v, targetJIDString, includeFromMe, func(record messageRecord) bool {
				return appendRecord(&messagesMu, &messageLog, seenMessages, record)
			}, println)
			if added > 0 {
				println("📚 Загружено сообщений из истории: %d", added)
			} else {
				println("📚 История чатов получена, подходящих сообщений не найдено")
			}
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

	println("\n📥 Последние сообщения за текущий запуск...")
	messagesMu.Lock()
	snapshot := snapshotRecords(messageLog, readLimit)
	messagesMu.Unlock()

	if len(snapshot) > 0 {
		fmt.Fprintln(out, "\n--- Последние сообщения ---")
		for i, msg := range snapshot {
			fmt.Fprintf(out, "\n%d) %s\n", i+1, msg.content)
		}
		fmt.Fprintln(out, "---------------------------")
		fmt.Fprintln(out)
	} else {
		fmt.Fprintln(out, "⚠️ Пока нет полученных сообщений в этой сессии")
	}

	if cfg.Message != "" {
		println("📤 Отправляю сообщение...")
		resp, err := client.SendMessage(context.Background(), targetJID, &waProto.Message{
			Conversation: proto.String(cfg.Message),
		})
		if err != nil {
			return result, fmt.Errorf("send message: %w", err)
		}
		result.MessageID = resp.ID
		println("✅ Сообщение отправлено! ID: %s", resp.ID)
	} else {
		println("📤 Текст сообщения не задан, отправка пропущена")
	}

	println("\n👂 Слушаю новые сообщения %d секунд...", int(listenAfterSend.Seconds()))
	time.Sleep(listenAfterSend)

	messagesMu.Lock()
	result.LastMessages = recordsToStrings(snapshotRecords(messageLog, readLimit))
	messagesMu.Unlock()

	println("\nОтключаюсь...")
	connected = false
	client.Disconnect()
	return result, nil
}

func resolveTargetJID(cfg Config) (types.JID, string, error) {
	chatIdentifier := strings.TrimSpace(cfg.Chat)
	if chatIdentifier != "" {
		jid, err := parseChatIdentifier(chatIdentifier)
		if err != nil {
			return types.JID{}, "", fmt.Errorf("resolve chat: %w", err)
		}
		return jid, jid.String(), nil
	}

	phone := strings.TrimSpace(cfg.PhoneNumber)
	if phone == "" {
		return types.JID{}, "", nil
	}

	jid := types.NewJID(phone, types.DefaultUserServer)
	return jid, jid.String(), nil
}

func parseChatIdentifier(value string) (types.JID, error) {
	if strings.Contains(value, "@") {
		jid, err := types.ParseJID(value)
		if err != nil {
			return types.JID{}, err
		}
		return jid, nil
	}

	return types.NewJID(value, types.DefaultUserServer), nil
}

func senderLabel(evt *events.Message) string {
	if evt.Info.IsFromMe {
		return "Ты"
	}

	if push := strings.TrimSpace(evt.Info.PushName); push != "" {
		return push
	}

	if evt.Info.Sender.User != "" {
		return evt.Info.Sender.User
	}

	return "Собеседник"
}

type messageRecord struct {
	key       string
	timestamp time.Time
	content   string
}

func newMessageRecord(evt *events.Message) (messageRecord, bool) {
	if evt == nil || evt.Message == nil {
		return messageRecord{}, false
	}

	text := strings.TrimSpace(extractMessageText(evt.Message))
	if text == "" {
		return messageRecord{}, false
	}

	timestamp := evt.Info.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	sender := senderLabel(evt)
	formatted := fmt.Sprintf("[%s] %s: %s",
		timestamp.Format("02.01.2006 15:04"),
		sender,
		text,
	)

	key := evt.Info.ID
	if key == "" {
		key = fmt.Sprintf("%s|%s|%t|%s",
			timestamp.UTC().Format(time.RFC3339Nano),
			sender,
			evt.Info.IsFromMe,
			text,
		)
	}

	return messageRecord{
		key:       key,
		timestamp: timestamp,
		content:   formatted,
	}, true
}

func extractMessageText(msg *waProto.Message) string {
	if msg == nil {
		return ""
	}

	if text := msg.GetConversation(); text != "" {
		return text
	}

	if ext := msg.GetExtendedTextMessage(); ext != nil {
		if text := ext.GetText(); text != "" {
			return text
		}
	}

	if img := msg.GetImageMessage(); img != nil {
		if caption := img.GetCaption(); caption != "" {
			return caption
		}
	}

	if video := msg.GetVideoMessage(); video != nil {
		if caption := video.GetCaption(); caption != "" {
			return caption
		}
	}

	if doc := msg.GetDocumentMessage(); doc != nil {
		if caption := doc.GetCaption(); caption != "" {
			return caption
		}
	}

	if buttons := msg.GetButtonsMessage(); buttons != nil {
		if text := buttons.GetContentText(); text != "" {
			return text
		}
		if footer := buttons.GetFooterText(); footer != "" {
			return footer
		}
	}

	if resp := msg.GetButtonsResponseMessage(); resp != nil {
		if text := resp.GetSelectedDisplayText(); text != "" {
			return text
		}
		if id := resp.GetSelectedButtonID(); id != "" {
			return id
		}
	}

	if list := msg.GetListResponseMessage(); list != nil {
		if text := list.GetTitle(); text != "" {
			return text
		}
		if reply := list.GetSingleSelectReply(); reply != nil {
			if selected := reply.GetSelectedRowID(); selected != "" {
				return selected
			}
		}
	}

	if tmpl := msg.GetTemplateButtonReplyMessage(); tmpl != nil {
		if text := tmpl.GetSelectedDisplayText(); text != "" {
			return text
		}
		if id := tmpl.GetSelectedID(); id != "" {
			return id
		}
	}

	if tmplResp := msg.GetTemplateMessage(); tmplResp != nil {
		if hydrated := tmplResp.GetHydratedTemplate(); hydrated != nil {
			if text := hydrated.GetHydratedContentText(); text != "" {
				return text
			}
			if title := hydrated.GetHydratedTitleText(); title != "" {
				return title
			}
			if footer := hydrated.GetHydratedFooterText(); footer != "" {
				return footer
			}
		}
	}

	return ""
}

func appendRecord(mu *sync.Mutex, records *[]messageRecord, seen map[string]struct{}, record messageRecord) bool {
	mu.Lock()
	defer mu.Unlock()

	if _, exists := seen[record.key]; exists {
		return false
	}

	seen[record.key] = struct{}{}
	*records = append(*records, record)
	return true
}

func processHistorySyncMessages(client *whatsmeow.Client, history *events.HistorySync, targetJID string, includeFromMe bool, add func(messageRecord) bool, logf func(string, ...interface{})) int {
	if history == nil || history.Data == nil {
		return 0
	}

	conversations := history.Data.GetConversations()
	added := 0

	for _, conv := range conversations {
		if conv == nil {
			continue
		}

		chatID := conv.GetID()
		if chatID == "" {
			continue
		}

		chatJID, err := types.ParseJID(chatID)
		if err != nil {
			logf("⚠️ Не удалось разобрать JID истории: %v", err)
			continue
		}

		if targetJID != "" && chatJID.String() != targetJID {
			continue
		}

		for _, historyMsg := range conv.GetMessages() {
			if historyMsg == nil || historyMsg.GetMessage() == nil {
				continue
			}

			evt, err := client.ParseWebMessage(chatJID, historyMsg.GetMessage())
			if err != nil {
				logf("⚠️ Не удалось обработать сообщение истории: %v", err)
				continue
			}

			if evt.Info.IsFromMe && !includeFromMe {
				continue
			}

			if record, ok := newMessageRecord(evt); ok {
				if add(record) {
					added++
				}
			}
		}
	}

	return added
}

func snapshotRecords(records []messageRecord, limit int) []messageRecord {
	if len(records) == 0 {
		return nil
	}

	snapshot := append([]messageRecord(nil), records...)
	sort.SliceStable(snapshot, func(i, j int) bool {
		if snapshot[i].timestamp.Equal(snapshot[j].timestamp) {
			return snapshot[i].content < snapshot[j].content
		}
		return snapshot[i].timestamp.Before(snapshot[j].timestamp)
	})

	if limit > 0 && len(snapshot) > limit {
		snapshot = snapshot[len(snapshot)-limit:]
	}

	return snapshot
}

func recordsToStrings(records []messageRecord) []string {
	if len(records) == 0 {
		return nil
	}

	out := make([]string, len(records))
	for i, record := range records {
		out[i] = record.content
	}

	return out
}
