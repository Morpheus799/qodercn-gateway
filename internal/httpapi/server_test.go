package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qodercn-gateway/internal/remote"
	"qodercn-gateway/internal/service"
	"qodercn-gateway/internal/tooltypes"
)

func TestNormalizeOpenAIRequestCollectsSystemMessages(t *testing.T) {
	req := openAIChatRequest{
		Model: "test-model",
		Messages: []rawMessage{
			{Role: "system", Content: "You are concise."},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi"},
			{Role: "system", Content: "Answer in Chinese."},
			{Role: "tool", Content: "ignored"},
			{Role: "user", Content: []any{
				map[string]any{"type": "text", "text": "Follow up"},
			}},
		},
	}

	normalized, err := normalizeOpenAIRequest(req)
	if err != nil {
		t.Fatalf("normalizeOpenAIRequest() error = %v", err)
	}
	if normalized.Model != "test-model" {
		t.Fatalf("model = %q", normalized.Model)
	}
	if normalized.System != "You are concise.\n\nAnswer in Chinese." {
		t.Fatalf("system = %q", normalized.System)
	}
	if len(normalized.Messages) != 3 {
		t.Fatalf("message count = %d", len(normalized.Messages))
	}
	if normalized.Messages[0].Role != "user" || normalized.Messages[0].Text != "Hello" {
		t.Fatalf("first message = %+v", normalized.Messages[0])
	}
	if normalized.Messages[1].Role != "assistant" || normalized.Messages[1].Text != "Hi" {
		t.Fatalf("second message = %+v", normalized.Messages[1])
	}
	if normalized.Messages[2].Role != "user" || normalized.Messages[2].Text != "Follow up" {
		t.Fatalf("third message = %+v", normalized.Messages[2])
	}
}

func TestNormalizeAnthropicRequestMapsThinkingToReasoningEffort(t *testing.T) {
	req := anthropicRequest{
		Model:     "Qwen3.6-Plus",
		MaxTokens: 256,
		Thinking: map[string]any{
			"type":          "enabled",
			"budget_tokens": 2048,
		},
		Messages: []rawMessage{
			{Role: "user", Content: "请先思考再回答"},
		},
	}

	normalized, err := normalizeAnthropicRequest(req)
	if err != nil {
		t.Fatalf("normalizeAnthropicRequest() error = %v", err)
	}
	if normalized.ReasoningEffort != "medium" {
		t.Fatalf("reasoning effort = %q", normalized.ReasoningEffort)
	}
}

func TestNormalizeAnthropicRequestAdaptiveThinkingEnablesReasoning(t *testing.T) {
	req := anthropicRequest{
		Model:     "Qwen3-Thinking",
		MaxTokens: 256,
		Thinking: map[string]any{
			"type": "adaptive",
		},
		Messages: []rawMessage{
			{Role: "user", Content: "请先思考再回答"},
		},
	}

	normalized, err := normalizeAnthropicRequest(req)
	if err != nil {
		t.Fatalf("normalizeAnthropicRequest() error = %v", err)
	}
	if normalized.ReasoningEffort != "medium" {
		t.Fatalf("reasoning effort = %q", normalized.ReasoningEffort)
	}
}

func TestShouldEmitThinkingHelpers(t *testing.T) {
	req := service.ChatRequest{ReasoningEffort: "medium"}
	result := &service.ChatResult{ThoughtText: "thought"}
	if !shouldEmitAnthropicThinking(req, result) {
		t.Fatal("expected anthropic thinking to emit")
	}
	if shouldEmitAnthropicThinking(service.ChatRequest{}, result) {
		t.Fatal("unexpected anthropic thinking without reasoning effort")
	}
}

func TestNormalizeOpenAIRequestRejectsMissingUserAndAssistantMessages(t *testing.T) {
	req := openAIChatRequest{
		Model: "test-model",
		Messages: []rawMessage{
			{Role: "system", Content: "Only system"},
			{Role: "tool", Content: "ignored"},
		},
	}

	_, err := normalizeOpenAIRequest(req)
	if err == nil {
		t.Fatal("expected error for request without user or assistant messages")
	}
}

func TestNormalizeAnthropicRequestExtractsStructuredText(t *testing.T) {
	req := anthropicRequest{
		Model:  "test-model",
		System: []any{map[string]any{"type": "text", "text": "System prompt"}},
		Messages: []rawMessage{
			{
				Role: "user",
				Content: []any{
					map[string]any{"type": "text", "text": "Hello"},
				},
			},
			{
				Role: "assistant",
				Content: []any{
					map[string]any{"type": "text", "text": "Hi"},
				},
			},
			{
				Role: "metadata",
				Content: []any{
					map[string]any{"type": "text", "text": "ignored"},
				},
			},
		},
	}

	normalized, err := normalizeAnthropicRequest(req)
	if err != nil {
		t.Fatalf("normalizeAnthropicRequest() error = %v", err)
	}
	if normalized.Model != "test-model" {
		t.Fatalf("model = %q", normalized.Model)
	}
	if normalized.System != "System prompt" {
		t.Fatalf("system = %q", normalized.System)
	}
	if len(normalized.Messages) != 2 {
		t.Fatalf("message count = %d", len(normalized.Messages))
	}
	if normalized.Messages[0].Role != "user" || normalized.Messages[0].Text != "Hello" {
		t.Fatalf("first message = %+v", normalized.Messages[0])
	}
	if normalized.Messages[1].Role != "assistant" || normalized.Messages[1].Text != "Hi" {
		t.Fatalf("second message = %+v", normalized.Messages[1])
	}
}

func TestNormalizeAnthropicRequestRejectsEmptyMessages(t *testing.T) {
	req := anthropicRequest{
		Model: "test-model",
		Messages: []rawMessage{
			{Role: "user", Content: ""},
			{Role: "assistant", Content: nil},
		},
	}

	_, err := normalizeAnthropicRequest(req)
	if err == nil {
		t.Fatal("expected error for request without usable messages")
	}
}

func defsContainTool(defs []any, name string) bool {
	for _, d := range defs {
		if m, ok := d.(map[string]any); ok && stringFromAny(m["name"]) == name {
			return true
		}
	}
	return false
}

// defsContainCallableTool reports whether defs has a model-callable tool:
// one with an input_schema and no hosted (server-executed) tool type.
func defsContainCallableTool(defs []any, name string) bool {
	for _, d := range defs {
		m, ok := d.(map[string]any)
		if !ok || stringFromAny(m["name"]) != name {
			continue
		}
		if _, hasSchema := m["input_schema"]; hasSchema && stringFromAny(m["type"]) == "" {
			return true
		}
	}
	return false
}

func TestAnthropicServerToolDefsWebSearch(t *testing.T) {
	req := anthropicRequest{
		Tools: []any{
			map[string]any{"name": "web_search", "type": "web_search_20250305"},
			map[string]any{"name": "Bash", "input_schema": map[string]any{"type": "object"}},
		},
	}
	defs, ok := (&Server{}).anthropicServerToolDefs(req)
	if !ok {
		t.Fatal("expected server tools to be offered for a hosted web_search request")
	}
	if !defsContainCallableTool(defs, webSearchToolName) {
		t.Fatalf("expected a callable web_search def, got %#v", defs)
	}
	if defsContainTool(defs, imageSearchToolName) {
		t.Fatal("ImageSearch should not be offered without the injection flag")
	}
}

func TestAnthropicServerToolDefsImageSearchFlag(t *testing.T) {
	t.Setenv("QODERCN_INJECT_MEDIA_TOOLS", "1")
	defs, ok := (&Server{}).anthropicServerToolDefs(anthropicRequest{})
	if !ok || !defsContainTool(defs, imageSearchToolName) || !defsContainTool(defs, textPolishToolName) {
		t.Fatalf("expected ImageSearch + TextPolish when the flag is set: ok=%v defs=%#v", ok, defs)
	}
	if defsContainTool(defs, webSearchToolName) {
		t.Fatal("web_search should not be offered unless the client declares it")
	}
}

func TestAnthropicServerToolDefsNone(t *testing.T) {
	req := anthropicRequest{Tools: []any{
		map[string]any{"name": "Bash", "input_schema": map[string]any{"type": "object"}},
	}}
	if _, ok := (&Server{}).anthropicServerToolDefs(req); ok {
		t.Fatal("no server tools should be offered without a hosted web_search or the flag")
	}
}

func TestInjectAnthropicServerToolsReplacesHostedWebSearch(t *testing.T) {
	req := anthropicRequest{Tools: []any{
		map[string]any{"name": "web_search", "type": "web_search_20250305"},
		map[string]any{"name": "Bash", "input_schema": map[string]any{"type": "object"}},
	}}
	defs, _ := (&Server{}).anthropicServerToolDefs(req)
	out := injectAnthropicServerTools(req, defs)
	tools, ok := out.Tools.([]any)
	if !ok {
		t.Fatalf("tools = %#v", out.Tools)
	}
	var hosted, callable, bash int
	for _, it := range tools {
		m, _ := it.(map[string]any)
		name := stringFromAny(m["name"])
		switch {
		case name == "web_search" && tooltypes.IsAnthropicHostedToolType(stringFromAny(m["type"])):
			hosted++
		case name == webSearchToolName:
			callable++
		case name == "Bash":
			bash++
		}
	}
	if hosted != 0 || callable != 1 || bash != 1 {
		t.Fatalf("hosted=%d callable=%d bash=%d tools=%#v", hosted, callable, bash, tools)
	}
	if !isServerTool(webSearchToolName) || !isServerTool(imageSearchToolName) || !isServerTool(textPolishToolName) || isServerTool("Bash") || isServerTool("web_search") {
		t.Fatal("isServerTool classification wrong")
	}
}

func TestServerToolUsageAccumulatesAcrossRounds(t *testing.T) {
	var u serverToolUsage
	u.add(&service.ChatResult{InputTokens: 100, OutputTokens: 10, CachedInputTokens: 20, ReasoningTokens: 5, UsedTokens: 110, Credits: 1.5, OriginalCredits: 2.0, Billable: true})
	u.add(&service.ChatResult{InputTokens: 150, OutputTokens: 30, UsedTokens: 180, Credits: 2.5, OriginalCredits: 3.0})
	u.add(nil) // tolerated
	got := u.result()
	if got.InputTokens != 250 || got.OutputTokens != 40 || got.UsedTokens != 290 {
		t.Fatalf("token sums wrong: in=%d out=%d total=%d", got.InputTokens, got.OutputTokens, got.UsedTokens)
	}
	if got.CachedInputTokens != 20 || got.ReasoningTokens != 5 {
		t.Fatalf("cached/reasoning sums wrong: cached=%d reasoning=%d", got.CachedInputTokens, got.ReasoningTokens)
	}
	if got.Credits != 4.0 || got.OriginalCredits != 5.0 || !got.Billable {
		t.Fatalf("credit sums wrong: credits=%v orig=%v billable=%v", got.Credits, got.OriginalCredits, got.Billable)
	}
	// The usage map must reflect the whole turn, not just the last round.
	um := openAIUsageMap(got)
	if um["prompt_tokens"] != 250 || um["completion_tokens"] != 40 || um["total_tokens"] != 290 || um["credits"] != 4.0 {
		t.Fatalf("openAIUsageMap did not aggregate: %#v", um)
	}
	// applyTo keeps the content result while replacing only the usage fields.
	merged := u.applyTo(&service.ChatResult{Text: "hi", FinishReason: "tool_calls"})
	if merged.Text != "hi" || merged.FinishReason != "tool_calls" || merged.InputTokens != 250 {
		t.Fatalf("applyTo should keep content and set aggregated usage: %#v", merged)
	}
}

func TestInjectOpenAIServerToolsSuffixedNoCollision(t *testing.T) {
	// A client tool named "web_search" must not collide with our suffixed one.
	req := service.ChatRequest{Tools: []tooltypes.ToolDef{{Name: "web_search"}}}
	out := injectOpenAIServerTools(req)
	var clientWeb, web, img, polish int
	for _, tool := range out.Tools {
		switch tool.Name {
		case "web_search":
			clientWeb++
		case webSearchToolName:
			web++
		case imageSearchToolName:
			img++
		case textPolishToolName:
			polish++
		}
	}
	if clientWeb != 1 {
		t.Fatalf("client's own web_search tool should be preserved once, got %d", clientWeb)
	}
	if web != 1 || img != 1 || polish != 1 {
		t.Fatalf("expected the three suffixed server tools added once each: web=%d img=%d polish=%d", web, img, polish)
	}
}

func TestDiscoveryCompatibilityEndpoints(t *testing.T) {
	server := NewServer("", service.New(service.Config{
		Model:   "Qwen3-Coder",
		Timeout: time.Second,
	}))

	cases := []string{
		"/version",
	}
	for _, path := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d body = %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestToolStreamFilterStreamsNormalTextWithTools(t *testing.T) {
	filter := newToolStreamFilter(true)
	var chunks []string
	chunks = append(chunks, filter.Push(strings.Repeat("你", 120))...)
	chunks = append(chunks, filter.Push("后续内容")...)
	chunks = append(chunks, filter.Flush()...)
	out := strings.Join(chunks, "")
	if !strings.Contains(out, "后续内容") {
		t.Fatalf("streamed text = %q", out)
	}
}

func TestShouldAggregateToolStreamRequiresOptIn(t *testing.T) {
	t.Setenv("QODERCN_AGGREGATE_TOOL_STREAM", "")
	req := service.ChatRequest{Tools: []tooltypes.ToolDef{{Name: "Bash"}}}
	if shouldAggregateToolStream(req) {
		t.Fatal("tool streams should remain incremental by default")
	}

	t.Setenv("QODERCN_AGGREGATE_TOOL_STREAM", "1")
	if !shouldAggregateToolStream(req) {
		t.Fatal("explicit aggregate env should enable aggregate tool streams")
	}
}

func TestToolStreamFilterBuffersActionBlock(t *testing.T) {
	filter := newToolStreamFilter(true)
	var chunks []string
	chunks = append(chunks, filter.Push("```json ")...)
	chunks = append(chunks, filter.Push("action\n{\"tool\":\"Bash\",\"parameters\":{\"command\":\"pwd\"}}\n```")...)
	chunks = append(chunks, filter.Flush()...)
	if len(chunks) != 0 {
		t.Fatalf("unexpected leaked action chunks: %#v", chunks)
	}
}

func TestParseImageURLRejectsLocalPaths(t *testing.T) {
	// A hosted proxy must never read host-local files on behalf of a request.
	// Every local-path form must be rejected (nil), even when the file exists.
	dir := t.TempDir()
	existing := filepath.Join(dir, "sample.png")
	if err := os.WriteFile(existing, []byte{0x89, 0x50, 0x4e, 0x47}, 0644); err != nil {
		t.Fatal(err)
	}
	rejected := []string{
		"file://" + existing,
		existing,
		"/etc/passwd",
		"~/.qoder-cn/.auth/user",
		"file:///etc/shadow",
		"../../etc/passwd",
	}
	for _, u := range rejected {
		if img := parseImageURL(u); img != nil {
			t.Errorf("parseImageURL(%q) = %+v, want nil (local reads must be refused)", u, img)
		}
	}
}

func TestOpenAIFinishReason(t *testing.T) {
	tool := []tooltypes.ToolCall{{}}
	cases := []struct {
		name   string
		result service.ChatResult
		want   string
	}{
		{"tool calls win", service.ChatResult{ToolCalls: tool, FinishReason: "length"}, "tool_calls"},
		{"length", service.ChatResult{FinishReason: "length"}, "length"},
		{"content filter", service.ChatResult{FinishReason: "content_filter"}, "content_filter"},
		{"stop passthrough", service.ChatResult{FinishReason: "stop"}, "stop"},
		{"empty defaults to stop", service.ChatResult{}, "stop"},
		{"unknown backend reason coerced to stop", service.ChatResult{FinishReason: "eos_token"}, "stop"},
		{"whitespace normalized", service.ChatResult{FinishReason: " length "}, "length"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := openAIFinishReason(&tc.result); got != tc.want {
				t.Fatalf("openAIFinishReason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAnthropicStopReason(t *testing.T) {
	tool := []tooltypes.ToolCall{{}}
	cases := []struct {
		name    string
		result  service.ChatResult
		reason  string
		seqWant any
	}{
		{"tool use", service.ChatResult{ToolCalls: tool, FinishReason: "stop"}, "tool_use", nil},
		{"length -> max_tokens", service.ChatResult{FinishReason: "length"}, "max_tokens", nil},
		{"stop with sequence", service.ChatResult{FinishReason: "stop", StopSequence: "\n\n"}, "stop_sequence", "\n\n"},
		{"stop without sequence", service.ChatResult{FinishReason: "stop"}, "end_turn", nil},
		{"content_filter -> refusal", service.ChatResult{FinishReason: "content_filter"}, "refusal", nil},
		{"unknown -> end_turn", service.ChatResult{FinishReason: "eos_token"}, "end_turn", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, seq := anthropicStopReason(&tc.result)
			if reason != tc.reason {
				t.Fatalf("stop_reason = %q, want %q", reason, tc.reason)
			}
			if seq != tc.seqWant {
				t.Fatalf("stop_sequence = %v, want %v", seq, tc.seqWant)
			}
		})
	}
}

func TestResolveAnthropicEffort(t *testing.T) {
	cases := []struct {
		name string
		req  anthropicRequest
		want string
	}{
		{"thinking.effort verbatim", anthropicRequest{Thinking: map[string]any{"type": "adaptive", "effort": "xhigh"}}, "xhigh"},
		{"output_config.effort", anthropicRequest{OutputConfig: map[string]any{"effort": "max"}}, "max"},
		{"top-level effort", anthropicRequest{Effort: "high"}, "high"},
		{"explicit output_config beats budget bucket", anthropicRequest{Thinking: map[string]any{"type": "enabled", "budget_tokens": float64(30000)}, OutputConfig: map[string]any{"effort": "medium"}}, "medium"},
		{"budget fallback bucket", anthropicRequest{Thinking: map[string]any{"type": "enabled", "budget_tokens": float64(500)}}, "low"},
		{"none passes through", anthropicRequest{Effort: "none"}, "none"},
		{"thinking disabled -> none", anthropicRequest{Thinking: map[string]any{"type": "disabled"}}, "none"},
		{"empty", anthropicRequest{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveAnthropicEffort(tc.req); got != tc.want {
				t.Fatalf("resolveAnthropicEffort = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReasoningEffortEnabled(t *testing.T) {
	for _, e := range []string{"", "none", "off", "disabled", "NONE", " off "} {
		if reasoningEffortEnabled(e) {
			t.Fatalf("reasoningEffortEnabled(%q) = true, want false", e)
		}
	}
	for _, e := range []string{"low", "medium", "high", "xhigh", "max"} {
		if !reasoningEffortEnabled(e) {
			t.Fatalf("reasoningEffortEnabled(%q) = false, want true", e)
		}
	}
}

func TestExtractAnthropicAssistantContentCapturesThinking(t *testing.T) {
	content := []any{
		map[string]any{"type": "thinking", "thinking": "step 1"},
		map[string]any{"type": "text", "text": "the answer"},
	}
	text, reasoning, _ := extractAnthropicAssistantContent(content)
	if text != "the answer" {
		t.Fatalf("text = %q, want %q", text, "the answer")
	}
	if reasoning != "step 1" {
		t.Fatalf("reasoning = %q, want %q", reasoning, "step 1")
	}
}

func TestNormalizeAnthropicRequestThinkingRoundTrip(t *testing.T) {
	req := anthropicRequest{
		Model: "m",
		Messages: []rawMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: []any{
				map[string]any{"type": "thinking", "thinking": "pondering"},
				map[string]any{"type": "text", "text": "hello"},
			}},
		},
	}
	cr, err := normalizeAnthropicRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range cr.Messages {
		if m.Role == "assistant" {
			found = true
			if m.ReasoningText != "pondering" {
				t.Fatalf("ReasoningText = %q, want %q", m.ReasoningText, "pondering")
			}
			if m.Text != "hello" {
				t.Fatalf("Text = %q, want %q", m.Text, "hello")
			}
		}
	}
	if !found {
		t.Fatal("no assistant message produced")
	}
}

type sseRec struct {
	typ         string
	index       int
	blockType   string
	id, name    string
	partialJSON string
	text        string
	thinking    string
}

func parseAnthropicSSE(t *testing.T, body string) []sseRec {
	t.Helper()
	str := func(v any) string {
		if v == nil {
			return ""
		}
		return fmt.Sprintf("%v", v)
	}
	var out []sseRec
	for _, block := range strings.Split(body, "\n\n") {
		var data string
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if data == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			continue
		}
		rec := sseRec{typ: str(m["type"])}
		if idx, ok := m["index"].(float64); ok {
			rec.index = int(idx)
		}
		if cb, ok := m["content_block"].(map[string]any); ok {
			rec.blockType = str(cb["type"])
			rec.id = str(cb["id"])
			rec.name = str(cb["name"])
		}
		if d, ok := m["delta"].(map[string]any); ok {
			rec.partialJSON = str(d["partial_json"])
			rec.text = str(d["text"])
			rec.thinking = str(d["thinking"])
		}
		out = append(out, rec)
	}
	return out
}

func runAnthropicStream(req service.ChatRequest, events []service.StreamEvent, final *service.ChatResult, emulateTextTools bool) string {
	evCh := make(chan service.StreamEvent, len(events)+1)
	for _, e := range events {
		evCh <- e
	}
	close(evCh)
	doneCh := make(chan service.StreamResult, 1)
	doneCh <- service.StreamResult{Result: final}
	close(doneCh)
	rec := httptest.NewRecorder()
	writeAnthropicStreamBody(context.Background(), rec, rec, req, evCh, doneCh, emulateTextTools)
	return rec.Body.String()
}

func TestWriteAnthropicStreamBodyIncrementalToolUse(t *testing.T) {
	req := service.ChatRequest{Tools: []tooltypes.ToolDef{{Name: "read_file"}}}
	events := []service.StreamEvent{
		{Type: service.StreamEventToolCall, ToolCall: &service.StreamToolCall{Index: 0, ID: "call_1", Name: "read_file"}},
		{Type: service.StreamEventToolCall, ToolCall: &service.StreamToolCall{Index: 0, ArgsFragment: `{"path":`}},
		{Type: service.StreamEventToolCall, ToolCall: &service.StreamToolCall{Index: 0, ArgsFragment: `"/a.txt"}`}},
	}
	final := &service.ChatResult{ToolCalls: []tooltypes.ToolCall{{ID: "call_1", Name: "read_file", Arguments: map[string]any{"path": "/a.txt"}}}, FinishReason: "tool_calls"}
	recs := parseAnthropicSSE(t, runAnthropicStream(req, events, final, false))

	starts, stops := 0, 0
	startIdx := -1
	var gotID, gotName, args string
	for _, r := range recs {
		if r.typ == "content_block_start" && r.blockType == "tool_use" {
			starts++
			startIdx = r.index
			gotID = r.id
			gotName = r.name
		}
		if r.typ == "content_block_delta" && r.partialJSON != "" {
			args += r.partialJSON
		}
	}
	for _, r := range recs {
		if r.typ == "content_block_stop" && r.index == startIdx {
			stops++
		}
	}
	if starts != 1 {
		t.Fatalf("tool_use content_block_start count = %d, want 1 (no duplicate aggregated block)", starts)
	}
	if startIdx != 0 {
		t.Fatalf("first tool_use block index = %d, want 0 (no hole at index 0 for a tool-only response)", startIdx)
	}
	if gotID != "call_1" || gotName != "read_file" {
		t.Fatalf("tool id/name = %q/%q, want call_1/read_file", gotID, gotName)
	}
	if args != `{"path":"/a.txt"}` {
		t.Fatalf("assembled args = %q, want %q", args, `{"path":"/a.txt"}`)
	}
	if stops != 1 {
		t.Fatalf("content_block_stop for tool block = %d, want 1", stops)
	}
}

func TestWriteAnthropicStreamBodyToolLateStart(t *testing.T) {
	req := service.ChatRequest{Tools: []tooltypes.ToolDef{{Name: "x"}}}
	events := []service.StreamEvent{
		{Type: service.StreamEventToolCall, ToolCall: &service.StreamToolCall{Index: 0, ArgsFragment: `{"a":1}`}},
	}
	final := &service.ChatResult{ToolCalls: []tooltypes.ToolCall{{}}, FinishReason: "tool_calls"}
	recs := parseAnthropicSSE(t, runAnthropicStream(req, events, final, false))
	startIdx := -1
	var gotID, gotName, args string
	for _, r := range recs {
		if r.typ == "content_block_start" && r.blockType == "tool_use" {
			startIdx = r.index
			gotID = r.id
			gotName = r.name
		}
		if r.typ == "content_block_delta" && r.partialJSON != "" {
			args += r.partialJSON
		}
	}
	if startIdx < 0 {
		t.Fatal("expected a late-started tool block")
	}
	if gotID != "tool_call_0" || gotName != "unknown_tool" {
		t.Fatalf("fallback id/name = %q/%q, want tool_call_0/unknown_tool", gotID, gotName)
	}
	if args != `{"a":1}` {
		t.Fatalf("late args = %q, want %q", args, `{"a":1}`)
	}
}

func TestWriteAnthropicStreamBodyEmulatedToolAggregated(t *testing.T) {
	req := service.ChatRequest{Tools: []tooltypes.ToolDef{{Name: "read_file"}}}
	final := &service.ChatResult{ToolCalls: []tooltypes.ToolCall{{ID: "call_9", Name: "read_file", Arguments: map[string]any{"path": "/b"}}}, FinishReason: "tool_calls"}
	recs := parseAnthropicSSE(t, runAnthropicStream(req, nil, final, false))
	starts := 0
	var gotID string
	for _, r := range recs {
		if r.typ == "content_block_start" && r.blockType == "tool_use" {
			starts++
			gotID = r.id
		}
	}
	if starts != 1 || gotID != "call_9" {
		t.Fatalf("aggregated tool_use starts=%d id=%q, want 1/call_9", starts, gotID)
	}
}

func TestWriteAnthropicStreamBodyThinkingTextToolCoexist(t *testing.T) {
	req := service.ChatRequest{ReasoningEffort: "high", Tools: []tooltypes.ToolDef{{Name: "read_file"}}}
	events := []service.StreamEvent{
		{Type: service.StreamEventThinking, Delta: "let me think about this"},
		{Type: service.StreamEventText, Delta: "checking now for you"},
		{Type: service.StreamEventToolCall, ToolCall: &service.StreamToolCall{Index: 0, ID: "call_1", Name: "read_file", ArgsFragment: `{}`}},
	}
	final := &service.ChatResult{ToolCalls: []tooltypes.ToolCall{{ID: "call_1", Name: "read_file"}}, FinishReason: "tool_calls"}
	recs := parseAnthropicSSE(t, runAnthropicStream(req, events, final, false))
	thinkingIdx, textIdx, toolIdx := -1, -1, -1
	for _, r := range recs {
		if r.typ == "content_block_start" {
			switch r.blockType {
			case "thinking":
				thinkingIdx = r.index
			case "text":
				textIdx = r.index
			case "tool_use":
				toolIdx = r.index
			}
		}
	}
	if thinkingIdx != 0 {
		t.Fatalf("thinking block index = %d, want 0", thinkingIdx)
	}
	if textIdx != 1 {
		t.Fatalf("text block index = %d, want 1", textIdx)
	}
	if toolIdx != 2 {
		t.Fatalf("tool block index = %d, want 2 (after thinking+text)", toolIdx)
	}
}

func TestNormalizeAnthropicRequestToolResultError(t *testing.T) {
	req := anthropicRequest{
		Model: "m",
		Messages: []rawMessage{
			{Role: "user", Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": "call_1", "is_error": true, "content": "boom"},
			}},
		},
	}
	cr, err := normalizeAnthropicRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var toolMsg *service.ChatMessage
	for i := range cr.Messages {
		if cr.Messages[i].Role == "tool" {
			toolMsg = &cr.Messages[i]
		}
	}
	if toolMsg == nil {
		t.Fatal("no tool message produced")
	}
	if !strings.HasPrefix(toolMsg.Text, "[tool_error]") {
		t.Fatalf("error not marked inline: %q", toolMsg.Text)
	}
	if toolMsg.ToolCallID != "call_1" {
		t.Fatalf("tool_call_id = %q, want call_1", toolMsg.ToolCallID)
	}
}

func TestEstimateAnthropicInputTokensExcludesImageData(t *testing.T) {
	bigB64 := strings.Repeat("A", 200000) // ~200k base64 chars; must NOT be counted rune-by-rune
	req := anthropicRequest{
		Model: "m",
		Messages: []rawMessage{{Role: "user", Content: []any{
			map[string]any{"type": "text", "text": "hi"},
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": bigB64}},
		}}},
	}
	got := estimateAnthropicInputTokens(req)
	if got > 5000 {
		t.Fatalf("estimate = %d; image base64 was counted (should be excluded, ~1600/image + text)", got)
	}
}

func TestNormalizeOpenAIRequestKeepsEmptyToolResultAndReasoning(t *testing.T) {
	req := openAIChatRequest{
		Model: "m",
		Messages: []rawMessage{
			{Role: "user", Content: "go"},
			{Role: "assistant", ReasoningContent: "thinking...", ToolCalls: []any{
				map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "f", "arguments": "{}"}},
			}},
			{Role: "tool", ToolCallID: "c1", Content: ""},
		},
	}
	cr, err := normalizeOpenAIRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var asst, tool *service.ChatMessage
	for i := range cr.Messages {
		switch cr.Messages[i].Role {
		case "assistant":
			asst = &cr.Messages[i]
		case "tool":
			tool = &cr.Messages[i]
		}
	}
	if asst == nil || asst.ReasoningText != "thinking..." {
		t.Fatalf("assistant reasoning not preserved: %#v", asst)
	}
	if tool == nil || tool.ToolCallID != "c1" || strings.TrimSpace(tool.Text) == "" {
		t.Fatalf("empty tool result dropped or unpaired: %#v", tool)
	}
}

func TestParseImageURLPassesRemoteURLThrough(t *testing.T) {
	img := parseImageURL("https://qoder-cn-vl.oss-cn-beijing.aliyuncs.com/x.png")
	if img == nil || img.URL != "https://qoder-cn-vl.oss-cn-beijing.aliyuncs.com/x.png" {
		t.Fatalf("remote URL not passed through: %#v", img)
	}
	if img.Data != "" {
		t.Fatalf("remote URL should not be downloaded/inlined: %#v", img)
	}
}

func TestExtractAnthropicImagesHandlesURLAndBase64(t *testing.T) {
	content := []any{
		map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://example.com/pic.png"}},
		map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "aGVsbG8="}},
	}
	imgs := extractAnthropicImages(content)
	if len(imgs) != 2 {
		t.Fatalf("want 2 images, got %d: %#v", len(imgs), imgs)
	}
	if imgs[0].URL != "https://example.com/pic.png" {
		t.Fatalf("URL-source image not passed through: %#v", imgs[0])
	}
	if imgs[1].Data == "" {
		t.Fatalf("base64 image lost its data: %#v", imgs[1])
	}
}

func TestCreditUsageExposedWhenCharged(t *testing.T) {
	result := &service.ChatResult{
		InputTokens:     10,
		OutputTokens:    20,
		Credits:         3.5,
		OriginalCredits: 7,
		Billable:        true,
	}
	for name, usage := range map[string]map[string]any{
		"openai":    openAIUsageMap(result),
		"anthropic": anthropicFinalUsage(result),
	} {
		if got := usage["credits"]; got != 3.5 {
			t.Errorf("%s: credits = %v, want 3.5", name, got)
		}
		if got := usage["original_credits"]; got != float64(7) {
			t.Errorf("%s: original_credits = %v, want 7", name, got)
		}
		if got := usage["billable"]; got != true {
			t.Errorf("%s: billable = %v, want true", name, got)
		}
	}
}

func TestCreditUsageOmittedWhenFree(t *testing.T) {
	result := &service.ChatResult{InputTokens: 10, OutputTokens: 20}
	for name, usage := range map[string]map[string]any{
		"openai":    openAIUsageMap(result),
		"anthropic": anthropicFinalUsage(result),
	} {
		if _, ok := usage["credits"]; ok {
			t.Errorf("%s: credits should be absent for a free turn", name)
		}
		if _, ok := usage["billable"]; ok {
			t.Errorf("%s: billable should be absent for a free turn", name)
		}
	}
}

func TestWithAuthKeyGate(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	// Disabled (no keys configured): pass through untouched.
	open := &Server{}
	rec := httptest.NewRecorder()
	open.withAuth(ok).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("disabled auth should pass through, got %d", rec.Code)
	}

	// Blank-only keys must not enable auth (fail-open would be a footgun, but
	// SetAuthKeys treats an all-blank list as "no keys").
	blank := &Server{}
	blank.SetAuthKeys([]string{" ", "", "\t"})
	if blank.authKeys != nil {
		t.Fatal("blank-only keys should leave auth disabled")
	}

	s := &Server{}
	s.SetAuthKeys([]string{"  ", "secret-key", ""})
	cases := []struct {
		name  string
		set   func(*http.Request)
		allow bool // true: expect 200; false: expect a silent connection abort
	}{
		{"no key", func(r *http.Request) {}, false},
		{"wrong bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") }, false},
		{"good bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer secret-key") }, true},
		{"scheme case-insensitive", func(r *http.Request) { r.Header.Set("Authorization", "bearer secret-key") }, true},
		{"good x-api-key", func(r *http.Request) { r.Header.Set("x-api-key", "secret-key") }, true},
		{"bare token", func(r *http.Request) { r.Header.Set("Authorization", "secret-key") }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			tc.set(req)
			rec := httptest.NewRecorder()
			if tc.allow {
				s.withAuth(ok).ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("authorized request: got %d want 200", rec.Code)
				}
				return
			}
			// Denied requests must abort the connection with no response body,
			// surfaced as a panic(http.ErrAbortHandler) at the handler layer.
			defer func() {
				switch v := recover(); v {
				case http.ErrAbortHandler:
					if rec.Code != 200 || rec.Body.Len() != 0 {
						t.Fatalf("denied request wrote a response: code=%d body=%q", rec.Code, rec.Body.String())
					}
				case nil:
					t.Fatalf("denied request did not abort (got response code %d)", rec.Code)
				default:
					t.Fatalf("unexpected panic: %v", v)
				}
			}()
			s.withAuth(ok).ServeHTTP(rec, req)
		})
	}
}

func TestStripAndFormatHostedWebSearch(t *testing.T) {
	tools := []any{
		map[string]any{"name": "web_search", "type": "web_search_20250305"},
		map[string]any{"name": "get_weather", "input_schema": map[string]any{"type": "object"}},
	}
	kept, ok := stripAnthropicHostedWebSearchTool(tools).([]any)
	if !ok || len(kept) != 1 {
		t.Fatalf("expected 1 client tool kept, got %#v", kept)
	}
	if m := kept[0].(map[string]any); m["name"] != "get_weather" {
		t.Fatalf("wrong tool kept: %#v", m)
	}
	// A regular client web_search (has input_schema, no hosted type) is kept.
	client := []any{map[string]any{"name": "web_search", "input_schema": map[string]any{}}}
	if got, _ := stripAnthropicHostedWebSearchTool(client).([]any); len(got) != 1 {
		t.Fatalf("client web_search should be kept: %#v", got)
	}
	// All-hosted strips to nil.
	if stripAnthropicHostedWebSearchTool([]any{map[string]any{"name": "web_search", "type": "web_search_20250305"}}) != nil {
		t.Fatal("all-hosted tools should strip to nil")
	}

	out := formatWebSearchResults("golang release", []remote.SearchResult{{Title: "Go", Link: "https://go.dev", Snippet: "released 2009"}})
	for _, want := range []string{"golang release", "[1] Go", "https://go.dev", "released 2009"} {
		if !strings.Contains(out, want) {
			t.Fatalf("formatted results missing %q:\n%s", want, out)
		}
	}
}
