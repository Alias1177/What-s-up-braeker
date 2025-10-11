package main

/*
#include <stdlib.h>
*/
import "C"
import (
	"context"
	"encoding/json"
	"fmt"
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

var lastReceivedMessages []string
var lastReceivedMessagesMu sync.Mutex

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

	lastReceivedMessagesMu.Lock()
	lastReceivedMessages = lastReceivedMessages[:0]
	lastReceivedMessagesMu.Unlock()

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

	// Обработчик входящих сообщений
	client.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			if v.Message == nil {
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
				lastReceivedMessagesMu.Lock()
				lastReceivedMessages = append(lastReceivedMessages, msg)
				lastReceivedMessagesMu.Unlock()
				fmt.Println("📥 Новое сообщение:", msg)
			}
		}
	})

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

	recipientJID := types.NewJID(goPhone, types.DefaultUserServer)

	// Если сообщение пустое - это режим чтения
	if goMessage == "" || len(goMessage) == 0 {
		fmt.Println("📖 Режим чтения (пустое сообщение)")

		fmt.Println("👂 Слушаю входящие сообщения 10 секунд...")
		time.Sleep(10 * time.Second)

		lastReceivedMessagesMu.Lock()
		if len(lastReceivedMessages) == 0 {
			fmt.Println("⚠️ Пока нет полученных сообщений в этой сессии")
		}

		resp.LastMessages = append([]string(nil), lastReceivedMessages...)
		lastReceivedMessagesMu.Unlock()
		return marshalResponse(resp)
	}

	// Режим отправки
	fmt.Printf("📤 Отправляю сообщение...\n")
	fmt.Printf("   Текст для отправки: '%s'\n", goMessage)
	fmt.Printf("   Получателю: %s\n", goPhone)

	// КРИТИЧНО: Создаём сообщение с явной проверкой
	if goMessage == "" {
		resp.Status = "error"
		resp.Error = "message is empty after conversion"
		return marshalResponse(resp)
	}

	msgToSend := &waProto.Message{
		Conversation: proto.String(goMessage),
	}

	// Проверяем что создали
	if msgToSend.Conversation == nil || *msgToSend.Conversation == "" {
		resp.Status = "error"
		resp.Error = "failed to create message proto"
		fmt.Println("❌ ОШИБКА: Conversation = nil или пустая!")
		return marshalResponse(resp)
	}

	fmt.Printf("✅ Proto сообщение создано: '%s'\n", *msgToSend.Conversation)

	sendResp, err := client.SendMessage(context.Background(), recipientJID, msgToSend)
	if err != nil {
		resp.Status = "error"
		resp.Error = fmt.Sprintf("failed to send: %v", err)
		return marshalResponse(resp)
	}

	fmt.Printf("✅ Сообщение отправлено! ID: %s\n", sendResp.ID)
	resp.MessageID = sendResp.ID

	// Слушаем новые сообщения
	fmt.Println("👂 Слушаю новые сообщения 10 секунд...")
	time.Sleep(10 * time.Second)

	lastReceivedMessagesMu.Lock()
	messagesCopy := append([]string(nil), lastReceivedMessages...)
	lastReceivedMessagesMu.Unlock()
	resp.LastMessages = messagesCopy

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
