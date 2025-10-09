package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"time"
	"unsafe"

	_ "github.com/mattn/go-sqlite3"

	"github.com/Alias1177/What-s-up-braeker/pkg/waclient"
)

type response struct {
	Status       string   `json:"status"`
	MessageID    string   `json:"message_id,omitempty"`
	LastMessages []string `json:"last_messages,omitempty"`
	RequiresQR   bool     `json:"requires_qr,omitempty"`
	Error        string   `json:"error,omitempty"`
}

func runClient(dbURI, phone, message string) *response {
	resp := &response{}

	if phone == "" {
		resp.Status = "error"
		resp.Error = "phone number is required"
		return resp
	}

	cfg := waclient.Config{
		DatabaseURI:     dbURI,
		PhoneNumber:     phone,
		Message:         message,
		WaitBeforeSend:  5 * time.Second,
		ListenAfterSend: 10 * time.Second,
	}

	result, err := waclient.Run(context.Background(), cfg)
	if err != nil {
		resp.Status = "error"
		resp.Error = err.Error()
		return resp
	}

	resp.Status = "ok"
	resp.MessageID = result.MessageID
	resp.LastMessages = result.LastMessages
	resp.RequiresQR = result.RequiresQR
	return resp
}

//export WaRun
func WaRun(dbURI *C.char, phone *C.char, message *C.char) *C.char {
	goDB := C.GoString(dbURI)
	if goDB == "" {
		goDB = "file:whatsapp.db?_foreign_keys=on"
	}
	goPhone := C.GoString(phone)
	goMessage := C.GoString(message)

	payload := runClient(goDB, goPhone, goMessage)
	data, err := json.Marshal(payload)
	if err != nil {
		data = []byte(`{"status":"error","error":"failed to marshal result"}`)
	}

	return C.CString(string(data))
}

//export WaFree
func WaFree(str *C.char) {
	if str != nil {
		C.free(unsafe.Pointer(str))
	}
}

func main() {}
