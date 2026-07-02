package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractFullAuditUserMessages_OpenAIChatExtractsAllUsers(t *testing.T) {
	body := []byte(`{
		"messages": [
			{"role":"system","content":"policy"},
			{"role":"user","content":"first"},
			{"role":"assistant","content":"ok"},
			{"role":"user","content":[{"type":"text","text":"second"}]}
		]
	}`)

	messages := ExtractFullAuditUserMessages(FullAuditProtocolOpenAIChat, body)

	require.Len(t, messages, 2)
	require.Contains(t, messages[0].Raw, `"first"`)
	require.Contains(t, messages[1].Raw, `"second"`)
	require.NotEmpty(t, messages[0].Hash)
	require.NotEqual(t, messages[0].Hash, messages[1].Hash)
}

func TestExtractFullAuditUserMessages_AnthropicExtractsAllUsers(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"one"},{"role":"assistant","content":"two"},{"role":"user","content":"three"}]}`)

	messages := ExtractFullAuditUserMessages(FullAuditProtocolAnthropicMessages, body)

	require.Len(t, messages, 2)
	require.Contains(t, messages[0].Raw, `"one"`)
	require.Contains(t, messages[1].Raw, `"three"`)
}

func TestExtractFullAuditUserMessages_ResponsesStringInput(t *testing.T) {
	body := []byte(`{"input":"hello world"}`)

	messages := ExtractFullAuditUserMessages(FullAuditProtocolOpenAIResponses, body)

	require.Len(t, messages, 1)
	require.Equal(t, `"hello world"`, messages[0].Raw)
}

func TestExtractFullAuditUserMessages_ResponsesArrayExtractsUserItems(t *testing.T) {
	body := []byte(`{
		"input": [
			{"role":"user","content":[{"type":"input_text","text":"one"}]},
			{"role":"assistant","content":"skip"},
			{"type":"input_text","text":"two"}
		]
	}`)

	messages := ExtractFullAuditUserMessages(FullAuditProtocolOpenAIResponses, body)

	require.Len(t, messages, 2)
	require.Contains(t, messages[0].Raw, `"one"`)
	require.Contains(t, messages[1].Raw, `"two"`)
}

func TestExtractFullAuditUserMessages_GeminiExtractsUserAndEmptyRole(t *testing.T) {
	body := []byte(`{
		"contents": [
			{"parts":[{"text":"implicit user"}]},
			{"role":"model","parts":[{"text":"skip"}]},
			{"role":"user","parts":[{"text":"explicit user"}]}
		]
	}`)

	messages := ExtractFullAuditUserMessages(FullAuditProtocolGemini, body)

	require.Len(t, messages, 2)
	require.Contains(t, messages[0].Raw, `"implicit user"`)
	require.Contains(t, messages[1].Raw, `"explicit user"`)
}

func TestExtractFullAuditUserMessages_EmptyInvalidAndNoUser(t *testing.T) {
	require.Nil(t, ExtractFullAuditUserMessages(FullAuditProtocolOpenAIChat, nil))
	require.Nil(t, ExtractFullAuditUserMessages(FullAuditProtocolOpenAIChat, []byte(`{`)))
	require.Nil(t, ExtractFullAuditUserMessages(FullAuditProtocolOpenAIChat, []byte(`{"messages":[{"role":"assistant","content":"ok"}]}`)))
}

func TestFullAuditMessageHashStable(t *testing.T) {
	raw := `{"role":"user","content":"same"}`

	require.Equal(t, FullAuditMessageHash(FullAuditProtocolOpenAIChat, raw), FullAuditMessageHash(FullAuditProtocolOpenAIChat, raw))
	require.NotEqual(t, FullAuditMessageHash(FullAuditProtocolOpenAIChat, raw), FullAuditMessageHash(FullAuditProtocolAnthropicMessages, raw))
}
