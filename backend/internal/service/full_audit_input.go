package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	FullAuditProtocolAnthropicMessages = "anthropic_messages"
	FullAuditProtocolOpenAIResponses   = "openai_responses"
	FullAuditProtocolOpenAIChat        = "openai_chat_completions"
	FullAuditProtocolGemini            = "gemini"
)

type FullAuditExtractedMessage struct {
	Hash string
	Role string
	Raw  string
	Size int
}

func ExtractFullAuditUserMessages(protocol string, body []byte) []FullAuditExtractedMessage {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil
	}
	var raws []string
	switch protocol {
	case FullAuditProtocolAnthropicMessages, FullAuditProtocolOpenAIChat:
		collectFullAuditRoleMessages(gjson.GetBytes(body, "messages"), "user", &raws)
	case FullAuditProtocolOpenAIResponses:
		collectFullAuditResponsesInput(gjson.GetBytes(body, "input"), &raws)
	case FullAuditProtocolGemini:
		collectFullAuditGeminiContents(gjson.GetBytes(body, "contents"), &raws)
	default:
		collectFullAuditRoleMessages(gjson.GetBytes(body, "messages"), "user", &raws)
		collectFullAuditResponsesInput(gjson.GetBytes(body, "input"), &raws)
		collectFullAuditGeminiContents(gjson.GetBytes(body, "contents"), &raws)
	}
	out := make([]FullAuditExtractedMessage, 0, len(raws))
	for _, raw := range raws {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		out = append(out, FullAuditExtractedMessage{
			Hash: FullAuditMessageHash(protocol, raw),
			Role: "user",
			Raw:  raw,
			Size: len(raw),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func collectFullAuditRoleMessages(messages gjson.Result, role string, raws *[]string) {
	if !messages.IsArray() {
		return
	}
	messages.ForEach(func(_, item gjson.Result) bool {
		if strings.EqualFold(strings.TrimSpace(item.Get("role").String()), role) {
			*raws = append(*raws, item.Raw)
		}
		return true
	})
}

func collectFullAuditResponsesInput(input gjson.Result, raws *[]string) {
	switch {
	case !input.Exists():
		return
	case input.Type == gjson.String:
		*raws = append(*raws, input.Raw)
	case input.IsArray():
		input.ForEach(func(_, item gjson.Result) bool {
			if isFullAuditResponsesUserItem(item) {
				*raws = append(*raws, item.Raw)
			}
			return true
		})
	case input.IsObject():
		if isFullAuditResponsesUserItem(input) {
			*raws = append(*raws, input.Raw)
		}
	}
}

func isFullAuditResponsesUserItem(item gjson.Result) bool {
	if !item.Exists() {
		return false
	}
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	if role == "user" {
		return true
	}
	if role != "" {
		return false
	}
	typ := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
	return typ == "input_text" || typ == "message" || item.Get("text").Exists()
}

func collectFullAuditGeminiContents(contents gjson.Result, raws *[]string) {
	if !contents.IsArray() {
		return
	}
	contents.ForEach(func(_, item gjson.Result) bool {
		role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
		if role == "" || role == "user" {
			*raws = append(*raws, item.Raw)
		}
		return true
	})
}

func FullAuditMessageHash(protocol string, raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(protocol) + "\x00" + raw))
	return hex.EncodeToString(sum[:])
}
