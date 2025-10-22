package waclient

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
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

type messageRecord struct {
	Timestamp time.Time
	Formatted string
}

func extractPlainText(msg *waProto.Message) string {
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

	if btn := msg.GetButtonsMessage(); btn != nil {
		if text := btn.GetContentText(); text != "" {
			return text
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

	if resp := msg.GetTemplateButtonReplyMessage(); resp != nil {
		if text := resp.GetSelectedDisplayText(); text != "" {
			return text
		}
		if id := resp.GetSelectedID(); id != "" {
			return id
		}
	}

	if resp := msg.GetListResponseMessage(); resp != nil {
		if title := resp.GetTitle(); title != "" {
			return title
		}
		if desc := resp.GetDescription(); desc != "" {
			return desc
		}
	}

	if poll := msg.GetPollCreationMessage(); poll != nil {
		if name := poll.GetName(); name != "" {
			return name
		}
	}

	if doc := msg.GetDocumentMessage(); doc != nil {
		if caption := doc.GetCaption(); caption != "" {
			return caption
		}
		if title := doc.GetTitle(); title != "" {
			return title
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

	if contact := msg.GetContactMessage(); contact != nil {
		if name := contact.GetDisplayName(); name != "" {
			return name
		}
	}

	if contacts := msg.GetContactsArrayMessage(); contacts != nil {
		list := contacts.GetContacts()
		for _, c := range list {
			if name := c.GetDisplayName(); name != "" {
				return name
			}
		}
	}

	if location := msg.GetLocationMessage(); location != nil {
		if name := location.GetName(); name != "" {
			return name
		}
		if address := location.GetAddress(); address != "" {
			return address
		}
	}

	if live := msg.GetLiveLocationMessage(); live != nil {
		if caption := live.GetCaption(); caption != "" {
			return caption
		}
	}

	if reaction := msg.GetReactionMessage(); reaction != nil {
		if text := reaction.GetText(); text != "" {
			return text
		}
	}

	if protocol := msg.GetProtocolMessage(); protocol != nil {
		if key := protocol.GetKey(); key != nil {
			if id := key.GetID(); id != "" {
				return id
			}
		}
	}

	return ""
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
		outputMu     sync.Mutex
	)
	println := func(format string, args ...interface{}) {
		outputMu.Lock()
		defer outputMu.Unlock()
		fmt.Fprintf(out, format+"\n", args...)
	}

	appendMessage := func(evt *events.Message) (string, bool) {
		if evt == nil || evt.Message == nil {
			return "", false
		}
		if evt.Info.Chat.String() != targetJIDString {
			return "", false
		}

		text := extractPlainText(evt.Message)
		if text == "" {
			return "", false
		}

		sender := "Собеседник"
		if evt.Info.IsFromMe {
			sender = "Ты"
		}

		formatted := fmt.Sprintf("[%s] %s: %s",
			evt.Info.Timestamp.Format("02.01.2006 15:04"),
			sender,
			text,
		)

		messagesMu.Lock()
		defer messagesMu.Unlock()

		msgID := string(evt.Info.ID)
		if msgID != "" {
			if _, exists := seenMessages[msgID]; exists {
				return "", false
			}
			seenMessages[msgID] = struct{}{}
		}

		messageLog = append(messageLog, messageRecord{
			Timestamp: evt.Info.Timestamp,
			Formatted: formatted,
		})
		sort.SliceStable(messageLog, func(i, j int) bool {
			return messageLog[i].Timestamp.Before(messageLog[j].Timestamp)
		})
		if readLimit > 0 && len(messageLog) > readLimit {
			messageLog = messageLog[len(messageLog)-readLimit:]
		}

		return formatted, true
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
			sender := "Собеседник"
			if v.Info.IsFromMe {
				sender = "Ты"
			}

		case *events.HistorySync:
			println("📚 Получена история чатов")
			if v.Data == nil {
				return
			}

			for _, conversation := range v.Data.GetConversations() {
				convID := conversation.GetID()
				if convID == "" {
					convID = conversation.GetNewJID()
				}
				if convID != targetJIDString {
					continue
				}

				historyEvents := make([]*events.Message, 0, len(conversation.GetMessages()))
				for _, historyMsg := range conversation.GetMessages() {
					parsed, err := client.ParseWebMessage(targetJID, historyMsg.GetMessage())
					if err != nil {
						println("⚠️ Не удалось разобрать сообщение истории: %v", err)
						continue
					}
					historyEvents = append(historyEvents, parsed)
				}

				sort.SliceStable(historyEvents, func(i, j int) bool {
					return historyEvents[i].Info.Timestamp.Before(historyEvents[j].Info.Timestamp)
				})

				for _, evtMsg := range historyEvents {
					if msg, ok := appendMessage(evtMsg); ok {
						println("📜 История: %s", msg)
					}
				}
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
	snapshot := append([]messageRecord(nil), messageLog...)
	messagesMu.Unlock()

	if len(snapshot) > 0 {
		fmt.Fprintln(out, "\n--- Последние сообщения ---")
		for i, msg := range snapshot {
			fmt.Fprintf(out, "\n%d) %s\n", i+1, msg.Formatted)
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
	for _, entry := range messageLog {
		result.LastMessages = append(result.LastMessages, entry.Formatted)
	}
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
