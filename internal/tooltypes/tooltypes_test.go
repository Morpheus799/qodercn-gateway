package tooltypes

import "testing"

func TestExtractToolsSupportsResponsesFunctionShape(t *testing.T) {
	tools := ExtractTools([]any{
		map[string]any{
			"type":        "function",
			"name":        "exec_command",
			"description": "Runs a command",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cmd": map[string]any{"type": "string"},
				},
				"required": []any{"cmd"},
			},
		},
	})
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "exec_command" {
		t.Fatalf("unexpected tool name %q", tools[0].Name)
	}
	props, _ := tools[0].InputSchema["properties"].(map[string]any)
	if _, ok := props["cmd"]; !ok {
		t.Fatalf("expected responses schema properties to be preserved")
	}
}

func TestExtractAnthropicToolsSkipsHostedWebSearch(t *testing.T) {
	tools := ExtractAnthropicTools([]any{
		map[string]any{
			"name": "web_search",
			"type": "web_search_20250305",
		},
		map[string]any{
			"name": "read_file",
			"input_schema": map[string]any{
				"type": "object",
			},
		},
	})
	if len(tools) != 1 {
		t.Fatalf("tool count = %d", len(tools))
	}
	if tools[0].Name != "read_file" {
		t.Fatalf("tool = %+v", tools[0])
	}
}

func TestExtractToolChoiceModes(t *testing.T) {
	cases := map[string]ToolChoice{
		"auto":     {Mode: "auto"},
		"none":     {Mode: "none"},
		"required": {Mode: "any"},
		"any":      {Mode: "any"},
		"my_tool":  {Mode: "tool", Name: "my_tool"},
	}
	for in, want := range cases {
		if got := ExtractToolChoice(in); got != want {
			t.Fatalf("ExtractToolChoice(%q) = %+v, want %+v", in, got, want)
		}
	}
	got := ExtractToolChoice(map[string]any{"type": "function", "function": map[string]any{"name": "fn"}})
	if got != (ToolChoice{Mode: "tool", Name: "fn"}) {
		t.Fatalf("function tool_choice = %+v", got)
	}
}

func TestExtractAnthropicToolChoiceModes(t *testing.T) {
	if got := ExtractAnthropicToolChoice(map[string]any{"type": "tool", "name": "read_file"}); got != (ToolChoice{Mode: "tool", Name: "read_file"}) {
		t.Fatalf("tool choice = %+v", got)
	}
	if got := ExtractAnthropicToolChoice(map[string]any{"type": "any"}); got != (ToolChoice{Mode: "any"}) {
		t.Fatalf("any choice = %+v", got)
	}
}
