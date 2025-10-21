package main

/*
#include <stdlib.h>
*/
import "C"
import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"

	_ "github.com/mattn/go-sqlite3"
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
}

const (
	defaultReadLimit     = 10
	defaultListenSeconds = 10.0
	defaultMessageBuffer = 50
	maxMessageBuffer     = 1000
)

type runPayload struct {
	SendText      string  `json:"send_text,omitempty"`
	ReadLimit     int     `json:"read_limit,omitempty"`
	ListenSeconds float64 `json:"listen_seconds,omitempty"`
}

type normalizedConfig struct {
	SendText          string
	ShouldSend        bool
	ShouldListen      bool
	ReadLimit         int
	ListenDuration    time.Duration
	explicitReadLimit bool
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
	payload, payloadProvided, err := parseRunPayload(raw)
	if err != nil {
		return normalizedConfig{}, fmt.Errorf("invalid request payload: %w", err)
	}

	sendText := strings.TrimSpace(payload.SendText)
	shouldSend := sendText != ""

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

	shouldListen := readLimit != 0 || listenDuration > 0 || !shouldSend
	if shouldListen && listenDuration <= 0 {
		listenDuration = time.Duration(defaultListenSeconds * float64(time.Second))
	}

	if shouldListen && readLimit == 0 && !explicitReadLimit {
		if listenSeconds == 0 && (payloadProvided || !shouldSend) {
			readLimit = defaultReadLimit
		}
	}

	return normalizedConfig{
		SendText:          sendText,
		ShouldSend:        shouldSend,
		ShouldListen:      shouldListen,
		ReadLimit:         readLimit,
		ListenDuration:    listenDuration,
		explicitReadLimit: explicitReadLimit,
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

	cfg, err := normalizeConfig(goMessage)
	if err != nil {
		resp.Status = "error"
		resp.Error = err.Error()
		return marshalResponse(resp)
	}

	if !cfg.ShouldSend && !cfg.ShouldListen {
		resp.Status = "error"
		resp.Error = "nothing to do: provide send_text or listening options"
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

	targetJID := types.NewJID(goPhone, types.DefaultUserServer)

	collector := newMessageCollector(cfg.ReadLimit, determineBufferCap(cfg.ReadLimit))

	handlerID := client.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			if v.Message == nil {
				return
			}
			if v.Info.Chat == nil || !v.Info.Chat.Equal(targetJID) {
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

	// Подключаемся
	err = client.Connect()
	if err != nil {
		resp.Status = "error"
		resp.Error = fmt.Sprintf("failed to connect: %v", err)
		return marshalResponse(resp)
	}
	defer client.Disconnect()

	fmt.Println("✅ Подключено к WhatsApp!")
	fmt.Println("Жду стабилизации соединения...")
	time.Sleep(3 * time.Second)

	if cfg.ShouldSend {
		fmt.Printf("📤 Отправляю сообщение...\n")
		fmt.Printf("   Текст для отправки: '%s'\n", cfg.SendText)
		fmt.Printf("   Получателю: %s\n", goPhone)

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

		sendResp, err := client.SendMessage(context.Background(), targetJID, msgToSend)
		if err != nil {
			resp.Status = "error"
			resp.Error = fmt.Sprintf("failed to send: %v", err)
			return marshalResponse(resp)
		}

		fmt.Printf("✅ Сообщение отправлено! ID: %s\n", sendResp.ID)
		resp.MessageID = sendResp.ID
	}

	if cfg.ShouldListen {
		listenMsg := fmt.Sprintf("👂 Слушаю входящие сообщения")
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

	fmt.Println("Отключаюсь...")
	return marshalResponse(resp)
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
