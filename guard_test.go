package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyImageCompatibilityRepairsStringifiedToolImage(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Inspect it"}]},
			{"type":"function_call_output","call_id":"call-1","output":"[{\"detail\":\"original\",\"image_url\":\"data:image/png;base64,QUJD\",\"type\":\"input_image\"}]"}
		]
	}`)

	out, modified, err := applyImageCompatibility(requestTransformRequest{
		FromFormat: "openai",
		ToFormat:   "codex",
		Model:      "gpt-5.6-sol",
		Body:       body,
	}, defaultGuardConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("expected request to be modified")
	}

	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	input := root["input"].([]any)
	output := input[1].(map[string]any)["output"]
	parts, ok := output.([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("expected structured output array, got %#v", output)
	}
	part := parts[0].(map[string]any)
	if part["type"] != "input_image" {
		t.Fatalf("expected input_image, got %#v", part)
	}
	if part["image_url"] != "data:image/png;base64,QUJD" {
		t.Fatalf("unexpected image URL: %#v", part["image_url"])
	}
	if part["detail"] != "original" {
		t.Fatalf("unexpected detail: %#v", part["detail"])
	}
}

func TestApplyImageCompatibilityTrimsOldestToolImagesAndKeepsManualImage(t *testing.T) {
	data := func(ch string) string {
		return "data:image/png;base64," + strings.Repeat(ch, 40)
	}
	request := map[string]any{
		"model": "gpt-5.6-sol",
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_image", "image_url": data("M")},
				},
			},
			map[string]any{"type": "function_call", "call_id": "call-a", "name": "view_image"},
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call-a",
				"output": []any{
					map[string]any{"type": "input_image", "image_url": data("A")},
				},
			},
			map[string]any{"type": "function_call", "call_id": "call-b", "name": "browser_screenshot"},
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call-b",
				"output": []any{
					map[string]any{"type": "input_image", "image_url": data("B")},
				},
			},
			map[string]any{"type": "function_call", "call_id": "call-c", "name": "view_image"},
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call-c",
				"output": []any{
					map[string]any{"type": "input_image", "image_url": data("C")},
				},
			},
		},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	cfg := defaultGuardConfig()
	cfg.MaxBase64Bytes = 95
	cfg.ContextWindowTokens = 272000
	cfg.ReserveTokens = 1
	cfg.EstimatedTokensPerImage = 1
	cfg.KeepRecentToolImages = 1

	out, modified, err := applyImageCompatibility(requestTransformRequest{
		FromFormat: "openai",
		ToFormat:   "codex",
		Model:      "p/gpt-5.6-sol",
		Body:       body,
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("expected request to be modified")
	}

	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	input := root["input"].([]any)
	manual := input[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if manual["type"] != "input_image" {
		t.Fatalf("manual image was changed: %#v", manual)
	}
	oldest := input[2].(map[string]any)["output"].([]any)[0].(map[string]any)
	if oldest["type"] != "input_text" {
		t.Fatalf("oldest tool image was not trimmed: %#v", oldest)
	}
	newest := input[6].(map[string]any)["output"].([]any)[0].(map[string]any)
	if newest["type"] != "input_image" {
		t.Fatalf("newest tool image was trimmed: %#v", newest)
	}
}

func TestApplyImageCompatibilityDoesNotTrimOtherToolImages(t *testing.T) {
	data := "data:image/png;base64," + strings.Repeat("A", 80)
	request := map[string]any{
		"input": []any{
			map[string]any{"type": "function_call", "call_id": "call-art", "name": "generate_image"},
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call-art",
				"output": []any{
					map[string]any{"type": "input_image", "image_url": data},
				},
			},
			map[string]any{"type": "function_call", "call_id": "call-shot", "name": "view_image"},
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call-shot",
				"output": []any{
					map[string]any{"type": "input_image", "image_url": data},
				},
			},
		},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultGuardConfig()
	cfg.MaxBase64Bytes = 1
	cfg.KeepRecentToolImages = 0

	out, modified, err := applyImageCompatibility(requestTransformRequest{
		FromFormat: "openai",
		ToFormat:   "codex",
		Model:      "gpt-5.6-sol",
		Body:       body,
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("expected screenshot to be trimmed")
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	input := root["input"].([]any)
	art := input[1].(map[string]any)["output"].([]any)[0].(map[string]any)
	if art["type"] != "input_image" {
		t.Fatalf("non-screenshot tool image was trimmed: %#v", art)
	}
	screenshot := input[3].(map[string]any)["output"].([]any)[0].(map[string]any)
	if screenshot["type"] != "input_text" {
		t.Fatalf("screenshot tool image was not trimmed: %#v", screenshot)
	}
}

func TestApplyImageCompatibilityCanTrimPreferredRecentImagesWhenNecessary(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"function_call","call_id":"call-shot","name":"view_image"},
		{"type":"function_call_output","call_id":"call-shot","output":[
			{"type":"input_image","image_url":"data:image/png;base64,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
		]}
	]}`)
	cfg := defaultGuardConfig()
	cfg.KeepRecentToolImages = 2
	cfg.MaxBase64Bytes = 1

	out, modified, err := applyImageCompatibility(requestTransformRequest{
		FromFormat: "openai",
		ToFormat:   "codex",
		Model:      "gpt-5.6-sol",
		Body:       body,
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("expected the only screenshot to be trimmed")
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	part := root["input"].([]any)[1].(map[string]any)["output"].([]any)[0].(map[string]any)
	if part["type"] != "input_text" {
		t.Fatalf("preferred recent screenshot was not trimmed under hard limit: %#v", part)
	}
}

func TestApplyImageCompatibilityBypassesOtherModelsAndFormats(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call_output","output":"[{\"type\":\"input_image\",\"image_url\":\"data:image/png;base64,QUJD\"}]"}]}`)
	tests := []requestTransformRequest{
		{FromFormat: "openai-response", ToFormat: "codex", Model: "gpt-5.6-sol", Body: body},
		{FromFormat: "openai", ToFormat: "codex", Model: "gpt-5.5", Body: body},
		{FromFormat: "openai", ToFormat: "gemini", Model: "gpt-5.6-sol", Body: body},
	}
	for _, req := range tests {
		out, modified, err := applyImageCompatibility(req, defaultGuardConfig())
		if err != nil {
			t.Fatal(err)
		}
		if modified || out != nil {
			t.Fatalf("request should have bypassed plugin: %+v", req)
		}
	}
}

func TestApplyImageCompatibilityLeavesOrdinaryJSONStringOutputAlone(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call_output","output":"{\"ok\":true}"}]}`)
	out, modified, err := applyImageCompatibility(requestTransformRequest{
		FromFormat: "openai",
		ToFormat:   "codex",
		Model:      "gpt-5.6-sol",
		Body:       body,
	}, defaultGuardConfig())
	if err != nil {
		t.Fatal(err)
	}
	if modified || out != nil {
		t.Fatalf("ordinary JSON string output should be unchanged: %s", out)
	}
}

func BenchmarkApplyImageCompatibilityBypass(b *testing.B) {
	body := []byte(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":"hello"}]}`)
	req := requestTransformRequest{
		FromFormat: "openai",
		ToFormat:   "codex",
		Model:      "gpt-5.5",
		Body:       body,
	}
	cfg := defaultGuardConfig()
	b.ReportAllocs()
	for range b.N {
		if _, _, err := applyImageCompatibility(req, cfg); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkApplyImageCompatibilityTargetNoImages(b *testing.B) {
	body := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":"hello"}]}`)
	req := requestTransformRequest{
		FromFormat: "openai",
		ToFormat:   "codex",
		Model:      "gpt-5.6-sol",
		Body:       body,
	}
	cfg := defaultGuardConfig()
	b.ReportAllocs()
	for range b.N {
		if _, _, err := applyImageCompatibility(req, cfg); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPluginRPCBoundaryBypass1MiB(b *testing.B) {
	padding := strings.Repeat("x", 1024*1024)
	req := requestTransformRequest{
		FromFormat: "openai",
		ToFormat:   "codex",
		Model:      "gpt-5.5",
		Body: []byte(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":"` +
			padding + `"}]}`),
	}
	b.SetBytes(int64(len(req.Body)))
	b.ReportAllocs()
	for range b.N {
		raw, err := json.Marshal(req)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := normalizeRequest(raw); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPluginRPCBoundaryTarget1MiB(b *testing.B) {
	padding := strings.Repeat("x", 1024*1024)
	req := requestTransformRequest{
		FromFormat: "openai",
		ToFormat:   "codex",
		Model:      "gpt-5.6-sol",
		Body: []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":"` +
			padding + `"}]}`),
	}
	b.SetBytes(int64(len(req.Body)))
	b.ReportAllocs()
	for range b.N {
		raw, err := json.Marshal(req)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := normalizeRequest(raw); err != nil {
			b.Fatal(err)
		}
	}
}
