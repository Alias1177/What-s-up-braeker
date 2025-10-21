package main

/*
#include <stdlib.h>
*/
import "C"
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unsafe"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

type Response struct {
	Status       string   `json:"status"`
	Error        string   `json:"error,omitempty"`
	MessageID    string   `json:"message_id,omitempty"`
	LastMessages []string `json:"last_messages,omitempty"`
	RequiresQR   bool     `json:"requires_qr,omitempty"`
}

const (
	defaultReadLimit     = 10
	defaultListenSeconds = 10.0
	defaultMessageBuffer = 50
	maxMessageBuffer     = 1000
)

type runPayload struct {
	SendText      string  `json:"send_text,omitempty"`
	Recipient     string  `json:"recipient,omitempty"`
	ReadChat      string  `json:"read_chat,omitempty"`
	ReadLimit     int     `json:"read_limit,omitempty"`
	ListenSeconds float64 `json:"listen_seconds,omitempty"`
	ShowQR        bool    `json:"show_qr,omitempty"`
	ForceRelink   bool    `json:"force_relink,omitempty"`
}

type normalizedConfig struct {
	SendText          string
	ShouldSend        bool
	Recipient         string
	ShouldListen      bool
	ReadChat          string
	ReadLimit         int
	ListenDuration    time.Duration
	explicitReadLimit bool
	ShowQR            bool
	ForceRelink       bool
}

type messageCollector struct {
	mu        sync.Mutex
	messages  []string
	bufferCap int
	limit     int
	done      chan struct{}
}

func newMessageCollector(limit, bufferCap int) *messageCollector {
	if bufferCap <= 0 {
		bufferCap = 1
	}
	var done chan struct{}
	if limit > 0 {
		done = make(chan struct{}, 1)
	}
	return &messageCollector{
		bufferCap: bufferCap,
		limit:     limit,
		done:      done,
	}
}

func (mc *messageCollector) add(msg string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	mc.messages = append(mc.messages, msg)
	if len(mc.messages) > mc.bufferCap {
		mc.messages = mc.messages[len(mc.messages)-mc.bufferCap:]
	}

	if mc.limit > 0 && len(mc.messages) >= mc.limit && mc.done != nil {
		select {
		case mc.done <- struct{}{}:
		default:
		}
	}
}

func (mc *messageCollector) snapshot() []string {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	result := make([]string, len(mc.messages))
	copy(result, mc.messages)
	return result
}

func parseRunPayload(raw string) (runPayload, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return runPayload{}, false, nil
	}

	if strings.HasPrefix(trimmed, "{") {
		var payload runPayload
		if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
			return runPayload{}, true, err
		}
		return payload, true, nil
	}

	return runPayload{SendText: raw}, false, nil
}

func normalizeConfig(raw string) (normalizedConfig, error) {
	payload, _, err := parseRunPayload(raw)
	if err != nil {
		return normalizedConfig{}, fmt.Errorf("invalid request payload: %w", err)
	}

	sendText := strings.TrimSpace(payload.SendText)
	recipient := strings.TrimSpace(payload.Recipient)
	if sendText != "" && recipient == "" {
		return normalizedConfig{}, fmt.Errorf("recipient is required when send_text is provided")
	}
	shouldSend := sendText != "" && recipient != ""

	readChat := strings.TrimSpace(payload.ReadChat)
	if readChat == "" {
		readChat = recipient
	}

	readLimit := payload.ReadLimit
	explicitReadLimit := readLimit != 0
	if readLimit < 0 {
		readLimit = 0
	}

	listenSeconds := payload.ListenSeconds
	if listenSeconds < 0 {
		listenSeconds = 0
	}

	listenDuration := time.Duration(listenSeconds * float64(time.Second))

	shouldListen := readChat != "" || readLimit > 0 || listenDuration > 0
	if shouldListen {
		if listenDuration <= 0 {
			listenDuration = time.Duration(defaultListenSeconds * float64(time.Second))
		}
		if readLimit == 0 && !explicitReadLimit && listenSeconds == 0 {
			readLimit = defaultReadLimit
		}
	}

	showQR := payload.ShowQR
	forceRelink := payload.ForceRelink

	if !shouldSend && !shouldListen && !showQR && !forceRelink {
		return normalizedConfig{}, fmt.Errorf("nothing to do: provide send_text, listening options, show_qr, or force_relink")
	}

	if shouldListen && readChat == "" {
		return normalizedConfig{}, fmt.Errorf("read_chat or recipient is required when listening for messages")
	}

	return normalizedConfig{
		SendText:          sendText,
		ShouldSend:        shouldSend,
		Recipient:         recipient,
		ShouldListen:      shouldListen,
		ReadChat:          readChat,
		ReadLimit:         readLimit,
		ListenDuration:    listenDuration,
		explicitReadLimit: explicitReadLimit,
		ShowQR:            showQR,
		ForceRelink:       forceRelink,
	}, nil
}

func determineBufferCap(limit int) int {
	bufferCap := defaultMessageBuffer
	if limit > bufferCap {
		bufferCap = limit
	}
	if bufferCap > maxMessageBuffer {
		bufferCap = maxMessageBuffer
	}
	return bufferCap
}

//export WaRun
func WaRun(dbURI, phone, message *C.char) *C.char {
	// Конвертируем C-строки в Go-строки
	goDBURI := C.GoString(dbURI)
	goPhone := C.GoString(phone)
	goMessage := C.GoString(message)

	// ВАЖНО: Логируем что получили
	fmt.Printf("[DEBUG] Получено от Python:\n")
	fmt.Printf("  DB: %s\n", goDBURI)
	fmt.Printf("  Phone: %s\n", goPhone)
	fmt.Printf("  Message: '%s' (длина: %d байт)\n", goMessage, len(goMessage))

	resp := &Response{Status: "ok"}
	ctx := context.Background()

	accountJID, err := parseAccountIdentifier(goPhone)
	if err != nil {
		resp.Status = "error"
		resp.Error = fmt.Sprintf("invalid account phone: %v", err)
		return marshalResponse(resp)
	}
	accountJIDString := accountJID.String()

	cfg, err := normalizeConfig(goMessage)
	if err != nil {
		resp.Status = "error"
		resp.Error = err.Error()
		return marshalResponse(resp)
	}

	if !cfg.ShouldSend && !cfg.ShouldListen && !cfg.ShowQR && !cfg.ForceRelink {
		resp.Status = "error"
		resp.Error = "nothing to do: provide send_text, listening options, show_qr, or force_relink"
		return marshalResponse(resp)
	}

	// Инициализация клиента
	log := waLog.Stdout("Client", "INFO", true)
	container, err := sqlstore.New(ctx, "sqlite3", goDBURI, log)
	if err != nil {
		resp.Status = "error"
		resp.Error = fmt.Sprintf("failed to init db: %v", err)
		return marshalResponse(resp)
	}
	defer container.Close()

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		resp.Status = "error"
		resp.Error = fmt.Sprintf("failed to get device: %v", err)
		return marshalResponse(resp)
	}

	client := whatsmeow.NewClient(deviceStore, log)

	resetSession := false
	if existing := client.Store.ID; existing != nil {
		if existing.String() != accountJIDString {
			if cfg.ForceRelink {
				fmt.Printf("⚠️ Сохранённая сессия относится к %s, запрошен доступ для %s. Перепривязываю...\n", existing.String(), accountJIDString)
				resetSession = true
			} else {
				resp.Status = "error"
				resp.Error = fmt.Sprintf("stored session is linked to %s, but %s was requested; rerun with force_relink=true to re-authorize", existing.String(), accountJIDString)
				return marshalResponse(resp)
			}
		} else if cfg.ForceRelink {
			fmt.Printf("🔁 Запрошена перепривязка для %s, сбрасываю текущую сессию...\n", accountJIDString)
			resetSession = true
		} else if cfg.ShowQR {
			fmt.Printf("ℹ️ show_qr=true, но сессия для %s уже активна — QR не требуется. Используйте force_relink=true, если нужно перепривязать.\n", accountJIDString)
		}
	} else {
		if cfg.ForceRelink {
			fmt.Printf("ℹ️ force_relink=true указан для %s, но сохранённой сессии нет — продолжаю стандартную авторизацию.\n", accountJIDString)
		}
		if cfg.ShowQR {
			fmt.Printf("ℹ️ Для %s нет активной сессии — QR будет показан после подключения\n", accountJIDString)
		}
	}

	if resetSession {
		if err := client.Store.Delete(ctx); err != nil {
			resp.Status = "error"
			resp.Error = fmt.Sprintf("failed to reset stored session: %v", err)
			return marshalResponse(resp)
		}

		deviceStore, err = container.GetFirstDevice(ctx)
		if err != nil {
			resp.Status = "error"
			resp.Error = fmt.Sprintf("failed to prepare device after reset: %v", err)
			return marshalResponse(resp)
		}
		client = whatsmeow.NewClient(deviceStore, log)
	}

	var sendTarget types.JID
	if cfg.ShouldSend {
		target, err := parseChatIdentifier(cfg.Recipient)
		if err != nil {
			resp.Status = "error"
			resp.Error = fmt.Sprintf("invalid recipient: %v", err)
			return marshalResponse(resp)
		}
		sendTarget = target
	}

	var (
		readTarget     types.JID
		haveReadTarget bool
		collector      *messageCollector
	)
	if cfg.ShouldListen {
		target, err := parseChatIdentifier(cfg.ReadChat)
		if err != nil {
			resp.Status = "error"
			resp.Error = fmt.Sprintf("invalid read_chat: %v", err)
			return marshalResponse(resp)
		}
		readTarget = target
		haveReadTarget = true
		collector = newMessageCollector(cfg.ReadLimit, determineBufferCap(cfg.ReadLimit))
	}

	handlerID := client.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			if collector == nil || v.Message == nil {
				return
			}
			if v.Info.Chat == nil {
				return
			}
			if haveReadTarget && !v.Info.Chat.Equal(readTarget) {
				return
			}
			text := v.Message.GetConversation()
			if text == "" && v.Message.ExtendedTextMessage != nil {
				text = v.Message.ExtendedTextMessage.GetText()
			}
			if text != "" {
				sender := "Собеседник"
				if v.Info.IsFromMe {
					sender = "Ты"
				}
				msg := fmt.Sprintf("[%s] %s", sender, text)
				fmt.Println("📥 Новое сообщение:", msg)
				collector.add(msg)
			}
		}
	})
	defer client.RemoveEventHandler(handlerID)

	connected := false
	defer func() {
		if connected {
			client.Disconnect()
		}
	}()

	resp.RequiresQR = client.Store.ID == nil
	if resp.RequiresQR {
		fmt.Printf("ℹ️ Требуется авторизация через QR-код для %s\n", accountJIDString)
	}
	if client.Store.ID == nil {
		qrChan, _ := client.GetQRChannel(context.Background())
		if err = client.Connect(); err != nil {
			resp.Status = "error"
			resp.Error = fmt.Sprintf("failed to connect (qr): %v", err)
			return marshalResponse(resp)
		}
		connected = true
		for evt := range qrChan {
			switch evt.Event {
			case "code":
				if cfg.ShowQR {
					fmt.Println("📱 Отсканируйте QR-код в WhatsApp:")
					qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
				} else {
					fmt.Println("ℹ️ Получен QR-код (show_qr=false, вывод пропущен)")
				}
			case "scan":
				fmt.Println("📸 QR-код отсканирован, ожидаем подтверждения...")
			case "timeout":
				fmt.Println("⏳ Срок действия QR истёк, получаем новый...")
			case "success":
				fmt.Println("✅ Авторизация по QR завершена")
			default:
				fmt.Printf("ℹ️ Событие QR: %s\n", evt.Event)
			}
		}
		fmt.Println("ℹ️ QR-канал закрыт, продолжаем работу")
		fmt.Println("✅ Подключено к WhatsApp!")
	} else {
		if err = client.Connect(); err != nil {
			resp.Status = "error"
			resp.Error = fmt.Sprintf("failed to connect: %v", err)
			return marshalResponse(resp)
		}
		connected = true
		fmt.Println("✅ Подключено к WhatsApp!")
	}

	if cfg.ShouldSend || cfg.ShouldListen {
		fmt.Println("Жду стабилизации соединения...")
		time.Sleep(3 * time.Second)
	}

	if cfg.ShouldSend {
		fmt.Printf("📤 Отправляю сообщение...\n")
		fmt.Printf("   Текст для отправки: '%s'\n", cfg.SendText)
		fmt.Printf("   Получателю: %s\n", sendTarget.String())

		msgToSend := &waProto.Message{
			Conversation: proto.String(cfg.SendText),
		}

		if msgToSend.Conversation == nil || *msgToSend.Conversation == "" {
			resp.Status = "error"
			resp.Error = "message is empty after conversion"
			fmt.Println("❌ ОШИБКА: Conversation = nil или пустая!")
			return marshalResponse(resp)
		}

		fmt.Printf("✅ Proto сообщение создано: '%s'\n", *msgToSend.Conversation)

		sendResp, err := client.SendMessage(context.Background(), sendTarget, msgToSend)
		if err != nil {
			resp.Status = "error"
			resp.Error = fmt.Sprintf("failed to send: %v", err)
			return marshalResponse(resp)
		}

		fmt.Printf("✅ Сообщение отправлено! ID: %s\n", sendResp.ID)
		resp.MessageID = sendResp.ID
	}

	if collector != nil {
		listenMsg := fmt.Sprintf("👂 Слушаю входящие сообщения для %s", readTarget.String())
		if cfg.ReadLimit > 0 {
			listenMsg += fmt.Sprintf(" (до %d сообщений)", cfg.ReadLimit)
		}
		if cfg.ListenDuration > 0 {
			listenMsg += fmt.Sprintf(" в течение %.1f сек.", cfg.ListenDuration.Seconds())
		}
		fmt.Println(listenMsg + "...")

		var timeout <-chan time.Time
		if cfg.ListenDuration > 0 {
			timer := time.NewTimer(cfg.ListenDuration)
			defer timer.Stop()
			timeout = timer.C
		}

		if cfg.ReadLimit > 0 && collector.done != nil {
			if timeout != nil {
				select {
				case <-collector.done:
				case <-timeout:
				}
			} else {
				<-collector.done
			}
		} else if timeout != nil {
			<-timeout
		}

		messages := collector.snapshot()
		if len(messages) == 0 {
			fmt.Println("⚠️ Пока нет полученных сообщений в этой сессии")
		}
		resp.LastMessages = messages
	}

	if connected {
		fmt.Println("Отключаюсь...")
	}
	return marshalResponse(resp)
}

func parseChatIdentifier(raw string) (types.JID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return types.JID{}, fmt.Errorf("empty chat identifier")
	}

	if strings.Contains(trimmed, "@") {
		jid, err := types.ParseJID(trimmed)
		if err != nil {
			return types.JID{}, err
		}
		return jid, nil
	}

	digits := digitsOnly(trimmed)
	if digits == "" {
		return types.JID{}, fmt.Errorf("no digits in chat identifier")
	}
	return types.NewJID(digits, types.DefaultUserServer), nil
}

func parseAccountIdentifier(raw string) (types.JID, error) {
	digits := digitsOnly(strings.TrimSpace(raw))
	if digits == "" {
		return types.JID{}, fmt.Errorf("no digits in phone")
	}
	return types.NewJID(digits, types.DefaultUserServer), nil
}

func digitsOnly(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func marshalResponse(resp *Response) *C.char {
	data, _ := json.Marshal(resp)
	result := C.CString(string(data))
	fmt.Printf("📦 Ответ библиотеки: %s\n", string(data))
	return result
}

//export WaFree
func WaFree(ptr *C.char) {
	C.free(unsafe.Pointer(ptr))
}

func main() {}
