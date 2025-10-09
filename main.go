package main

import (
	"context"
	"fmt"
	"os"
	"time"

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

var lastMessages []string

func main() {
	// Логгер
	log := waLog.Stdout("Client", "INFO", true)
	ctx := context.Background()

	//БД сессий
	container, err := sqlstore.New(ctx, "sqlite3", "file:whatsapp.db?_foreign_keys=on", log)
	if err != nil {
		panic(err)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		panic(err)
	}

	client := whatsmeow.NewClient(deviceStore, log)

	//Ручка получения сообщений
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
				lastMessages = append(lastMessages, msg)
				fmt.Println("📩 Новое сообщение:", msg)
			}

		case *events.HistorySync:
			fmt.Println("📚 Получена история чатов")
			// Здесь можно обрабатывать синхронизацию истории
		}
	})

	// Если не авторизован - показываем QR код
	if client.Store.ID == nil {
		qrChan, _ := client.GetQRChannel(context.Background())
		err = client.Connect()
		if err != nil {
			panic(err)
		}

		fmt.Println("Отсканируй QR-код в WhatsApp:")
		for evt := range qrChan {
			if evt.Event == "code" {
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			} else {
				fmt.Println("Событие:", evt.Event)
			}
		}
	} else {
		// Уже авторизован, просто подключаемся
		err = client.Connect()
		if err != nil {
			panic(err)
		}
		fmt.Println("✅ Подключено к WhatsApp!")
	}

	// Ждём стабильности
	fmt.Println("Жду стабилизации соединения...")
	time.Sleep(5 * time.Second)

	// НАСТРОЙ ТУТ:
	phoneNumber := "994516995516" // Номер в формате: код страны + номер (без +)
	message := "Привет с Linux сервера! 🐱"

	// Формируем JID (WhatsApp ID)
	recipientJID := types.NewJID(phoneNumber, types.DefaultUserServer)

	// Выводим накопленные в текущей сессии сообщения
	fmt.Println("\n📥 Последние сообщения за текущий запуск...")
	if len(lastMessages) > 0 {
		fmt.Println("\n--- Последние сообщения ---")
		for i, msg := range lastMessages {
			fmt.Printf("\n%d) %s\n", i+1, msg)
		}
		fmt.Println("---------------------------\n")
	} else {
		fmt.Println("⚠️ Пока нет полученных сообщений в этой сессии")
	}

	// ОТПРАВЛЯЕМ СООБЩЕНИЕ
	fmt.Println("📤 Отправляю сообщение...")

	resp, err := client.SendMessage(context.Background(), recipientJID, &waProto.Message{
		Conversation: proto.String(message),
	})

	if err != nil {
		fmt.Println("❌ Ошибка отправки:", err)
	} else {
		fmt.Printf("✅ Сообщение отправлено! ID: %s\n", resp.ID)
	}

	// Ждём новые сообщения 10 секунд
	fmt.Println("\n👂 Слушаю новые сообщения 10 секунд...")
	time.Sleep(10 * time.Second)

	fmt.Println("\nОтключаюсь...")
	client.Disconnect()
}
