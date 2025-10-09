package main

import (
	"context"
	"fmt"
	"os"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/Alias1177/What-s-up-braeker/pkg/waclient"
)

func main() {
	ctx := context.Background()

	phoneNumber := "994516995516" // Номер в формате: код страны + номер (без +)
	message := "Привет с Linux сервера! 🐱"

	result, err := waclient.Run(ctx, waclient.Config{
		DatabaseURI:     "file:whatsapp.db?_foreign_keys=on",
		PhoneNumber:     phoneNumber,
		Message:         message,
		WaitBeforeSend:  5 * time.Second,
		ListenAfterSend: 10 * time.Second,
		Output:          os.Stdout,
		QRWriter:        os.Stdout,
		LogEnableColor:  true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Ошибка: %v\n", err)
		os.Exit(1)
	}

	if result.MessageID != "" {
		fmt.Printf("\nИдентификатор отправленного сообщения: %s\n", result.MessageID)
	}

	if len(result.LastMessages) > 0 {
		fmt.Println("\nСводка сообщений, полученных в этой сессии:")
		for i, msg := range result.LastMessages {
			fmt.Printf("%d) %s\n", i+1, msg)
		}
	}
}
