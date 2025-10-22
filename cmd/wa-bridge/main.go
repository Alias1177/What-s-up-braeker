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
	"time"
	"unsafe"

	_ "github.com/mattn/go-sqlite3"

	"github.com/Alias1177/What-s-up-braeker/pkg/waclient"
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
)

type runPayload struct {
	SendText      string  `json:"send_text,omitempty"`
	ReadLimit     int     `json:"read_limit,omitempty"`
	ListenSeconds float64 `json:"listen_seconds,omitempty"`
	FilterChat    string  `json:"filter_chat,omitempty"`
	IncludeFromMe *bool   `json:"include_from_me,omitempty"`
}

type normalizedConfig struct {
	SendText       string
	ShouldSend     bool
	ShouldListen   bool
	ReadLimit      int
	ListenDuration time.Duration
	FilterChat     string
	IncludeFromMe  bool
	IncludeSet     bool
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

	filterChat := strings.TrimSpace(payload.FilterChat)

	includeFromMe := true
	includeSet := false
	if payload.IncludeFromMe != nil {
		includeFromMe = *payload.IncludeFromMe
		includeSet = true
	}

	listenSeconds := payload.ListenSeconds
	if listenSeconds < 0 {
		listenSeconds = 0
	}

	listenDuration := time.Duration(listenSeconds * float64(time.Second))

	shouldListen := readLimit != 0 || listenDuration > 0 || !shouldSend
	if shouldListen {
		if listenDuration <= 0 {
			listenDuration = time.Duration(defaultListenSeconds * float64(time.Second))
		}
		if readLimit == 0 && !explicitReadLimit {
			if listenSeconds == 0 && (payloadProvided || !shouldSend) {
				readLimit = defaultReadLimit
			}
		}
	} else {
		readLimit = 0
		listenDuration = 0
	}

	return normalizedConfig{
		SendText:       sendText,
		ShouldSend:     shouldSend,
		ShouldListen:   shouldListen,
		ReadLimit:      readLimit,
		ListenDuration: listenDuration,
		FilterChat:     filterChat,
		IncludeFromMe:  includeFromMe,
		IncludeSet:     includeSet,
	}, nil
}

//export WaRun
func WaRun(dbURI, phone, message *C.char) *C.char {
	goDBURI := strings.TrimSpace(C.GoString(dbURI))
	goPhone := strings.TrimSpace(C.GoString(phone))
	goMessage := C.GoString(message)

	resp := &Response{Status: "ok"}

	cfg, err := normalizeConfig(goMessage)
	if err != nil {
		resp.Status = "error"
		resp.Error = err.Error()
		return marshalResponse(resp)
	}

	if goPhone == "" && cfg.FilterChat == "" {
		resp.Status = "error"
		resp.Error = "phone number or filter_chat is required"
		return marshalResponse(resp)
	}

	waConfig := waclient.Config{
		DatabaseURI:      goDBURI,
		PhoneNumber:      goPhone,
		Chat:             cfg.FilterChat,
		ReadLimit:        cfg.ReadLimit,
		IncludeFromMe:    cfg.IncludeFromMe,
		IncludeFromMeSet: cfg.IncludeSet,
	}
	if cfg.ShouldSend {
		waConfig.Message = cfg.SendText
	}
	if cfg.ShouldListen {
		waConfig.ListenAfterSend = cfg.ListenDuration
	} else {
		waConfig.ListenAfterSend = time.Second
	}

	result, runErr := waclient.Run(context.Background(), waConfig)
	if runErr != nil {
		resp.Status = "error"
		resp.Error = runErr.Error()
		return marshalResponse(resp)
	}

	resp.MessageID = result.MessageID
	resp.LastMessages = append(resp.LastMessages, result.LastMessages...)
	resp.RequiresQR = result.RequiresQR
	return marshalResponse(resp)
}

func marshalResponse(resp *Response) *C.char {
	data, _ := json.Marshal(resp)
	return C.CString(string(data))
}

//export WaFree
func WaFree(ptr *C.char) {
	C.free(unsafe.Pointer(ptr))
}

func main() {}
