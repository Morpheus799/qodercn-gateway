package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"qodercn-gateway/internal/remote"
	"qodercn-gateway/internal/toolemulation"
)

func TestNewKeepsZeroTimeoutUnlimited(t *testing.T) {
	svc := New(Config{Timeout: 0})
	if svc.cfg.Timeout != 0 {
		t.Fatalf("timeout = %v, want 0", svc.cfg.Timeout)
	}
}

func TestContextWithOptionalTimeoutZeroDoesNotSetDeadline(t *testing.T) {
	ctx, cancel := contextWithOptionalTimeout(context.Background(), 0)
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("zero timeout should not set a deadline")
	}
}

func TestContextWithOptionalTimeoutPositiveSetsDeadline(t *testing.T) {
	ctx, cancel := contextWithOptionalTimeout(context.Background(), time.Second)
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("positive timeout should set a deadline")
	}
}

func TestNormalizeRemoteModelMapsFriendlyNames(t *testing.T) {
	cases := map[string]string{
		"Kimi-K2.6":    "kmodel",
		"MiniMax-M2.7": "mmodel",
		"auto":         "org_auto",
		"unknown-xyz":  "unknown-xyz",
		"":             "",
	}
	for in, want := range cases {
		if got := normalizeRemoteModel(in); got != want {
			t.Fatalf("normalizeRemoteModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEmulatesTextToolsAlwaysFalse(t *testing.T) {
	svc := New(Config{})
	req := ChatRequest{
		Tools:      []toolemulation.ToolDef{{Name: "Bash"}},
		ToolChoice: toolemulation.ToolChoice{Mode: "auto"},
	}
	if svc.EmulatesTextTools(req) {
		t.Fatal("native gateway path must never emulate text tools")
	}
}

func TestBuildLingmaPromptOmitsToolEmulation(t *testing.T) {
	req := ChatRequest{
		Messages: []ChatMessage{{Role: "user", Text: "查看项目结构"}},
		Tools: []toolemulation.ToolDef{{
			Name: "Bash",
			InputSchema: map[string]any{
				"properties": map[string]any{
					"command": map[string]any{"type": "string"},
				},
				"required": []any{"command"},
			},
		}},
		ToolChoice: toolemulation.ToolChoice{Mode: "auto"},
	}
	prompt, err := buildLingmaPrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "```json action") || strings.Contains(prompt, "DIRECT tool access") {
		t.Fatalf("prompt should not include tool emulation:\n%s", prompt)
	}
}

func TestBuildLingmaPromptIncludesReasoningHintOnlyWhenRequested(t *testing.T) {
	req := ChatRequest{
		Messages:        []ChatMessage{{Role: "user", Text: "解释这个函数"}},
		ReasoningEffort: "high",
	}
	prompt, err := buildLingmaPrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "Reasoning mode is enabled") {
		t.Fatalf("prompt should include reasoning hint:\n%s", prompt)
	}

	plainPrompt, err := buildLingmaPrompt(ChatRequest{
		Messages: []ChatMessage{{Role: "user", Text: "解释这个函数"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plainPrompt, "Reasoning mode is enabled") {
		t.Fatalf("plain prompt should not include reasoning hint:\n%s", plainPrompt)
	}
}

func TestShouldRetryRemoteNativeToolForContinuationText(t *testing.T) {
	req := ChatRequest{
		Tools: []toolemulation.ToolDef{{Name: "Bash"}},
		ToolChoice: toolemulation.ToolChoice{
			Mode: "auto",
		},
	}
	if !shouldRetryRemoteNativeTool(req, "让我查看一下项目的整体结构，特别是源代码目录：") {
		t.Fatal("expected continuation text to trigger native tool retry")
	}
	if shouldRetryRemoteNativeTool(req, "这是一个 uni-app 项目，核心目录是 src。") {
		t.Fatal("substantive answer should not trigger retry")
	}
	req.ToolChoice = toolemulation.ToolChoice{Mode: "none"}
	if shouldRetryRemoteNativeTool(req, "让我查看一下：") {
		t.Fatal("tool_choice none should not trigger retry")
	}
}

func TestRemoteImagesFromRequest(t *testing.T) {
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Text: "see", Images: []Image{{MediaType: "image/png", Data: "AAAA"}}}}}
	images := remoteImagesFromRequest(req)
	if len(images) != 1 {
		t.Fatalf("images = %#v", images)
	}
	if images[0].MediaType != "image/png" || images[0].Data != "AAAA" {
		t.Fatalf("unexpected image = %#v", images[0])
	}
}

func TestBuildLingmaPromptUsesImageFallbackForImageOnlyUser(t *testing.T) {
	req := ChatRequest{
		System:   "这张图片是什么？只用两句话回答。",
		Messages: []ChatMessage{{Role: "user", Images: []Image{{URL: "file:///tmp/a.jpg"}}}},
	}

	prompt, err := buildLingmaPrompt(req)
	if err != nil {
		t.Fatalf("buildLingmaPrompt returned error: %v", err)
	}
	if !strings.Contains(prompt, "这张图片是什么") {
		t.Fatalf("prompt should include image fallback question, got %q", prompt)
	}
}

func TestRemoteMessagesFromRequestMapsReasoningText(t *testing.T) {
	req := ChatRequest{Messages: []ChatMessage{
		{Role: "assistant", Text: "answer", ReasoningText: "prior thinking"},
	}}
	out := remoteMessagesFromRequest(req)
	if len(out) != 1 {
		t.Fatalf("message count = %d", len(out))
	}
	if out[0].ReasoningText != "prior thinking" {
		t.Fatalf("ReasoningText = %q, want %q", out[0].ReasoningText, "prior thinking")
	}
}

func TestRemoteMessagesFromRequestKeepsReasoningOnlyTurn(t *testing.T) {
	req := ChatRequest{Messages: []ChatMessage{
		{Role: "assistant", ReasoningText: "only thinking"},
	}}
	out := remoteMessagesFromRequest(req)
	if len(out) != 1 || out[0].ReasoningText != "only thinking" {
		t.Fatalf("reasoning-only turn dropped or wrong: %#v", out)
	}
}

func TestRemoteMessagesFromRequestPreservesToolTurnsAndImages(t *testing.T) {
	req := ChatRequest{
		Tools:      []toolemulation.ToolDef{{Name: "get_image"}},
		ToolChoice: toolemulation.ToolChoice{Mode: "auto"},
		Messages: []ChatMessage{
			{Role: "user", Text: "look at this"},
			{Role: "assistant", ToolCalls: []toolemulation.ToolCall{{ID: "c1", Name: "get_image"}}},
			{Role: "tool", ToolCallID: "c1", Text: "here", Images: []Image{{MediaType: "image/png", Data: "abc"}}},
		},
	}
	out := remoteMessagesFromRequest(req)
	if len(out) < 3 {
		t.Fatalf("structured path should keep all messages, got %d", len(out))
	}
	foundTool := false
	for _, m := range out {
		if m.Role == "tool" {
			foundTool = true
			if m.ToolCallID != "c1" || len(m.Images) == 0 {
				t.Fatalf("tool message lost id/images: %#v", m)
			}
		}
	}
	if !foundTool {
		t.Fatal("structured path dropped the tool-role message")
	}
}

func TestResolveRemoteModelUsesCachedUpstreamList(t *testing.T) {
	svc := New(Config{Timeout: time.Second})
	svc.remoteModelsMu.Lock()
	svc.remoteModels = []remote.Model{
		{Key: "gm51model", DisplayName: "GLM-5.2"},
		{Key: "qmodel_38max", DisplayName: "Qwen3.8-Max"},
	}
	svc.remoteModelsAt = time.Now()
	svc.remoteModelsMu.Unlock()
	ctx := context.Background()
	cases := map[string]string{
		"gm51model":           "gm51model",           // exact key
		"GLM-5.2":             "gm51model",           // display name
		"qwen3.8-max":         "qmodel_38max",        // case-insensitive display name
		"totally-unknown-xyz": "totally-unknown-xyz", // unknown -> passthrough (client's problem)
	}
	for in, want := range cases {
		if got := svc.resolveRemoteModel(ctx, in); got != want {
			t.Fatalf("resolveRemoteModel(%q) = %q, want %q", in, got, want)
		}
	}
}
