package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func normalizeBody(t *testing.T, body []byte) ([]byte, bool) {
	t.Helper()
	out, modified, err := applyImageCompatibility(requestTransformRequest{
		FromFormat: "openai",
		ToFormat:   "codex",
		Model:      "p/gpt-5.6-sol",
		Body:       body,
	}, defaultCompatibilityConfig())
	if err != nil {
		t.Fatal(err)
	}
	return out, modified
}

func decodedInput(t *testing.T, body []byte) []any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	input, ok := root["input"].([]any)
	if !ok {
		t.Fatalf("input is not an array: %#v", root["input"])
	}
	return input
}

func TestApplyImageCompatibilityRepairsFlatInputImage(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"function_call_output","call_id":"call-1","output":"[{\"detail\":\"original\",\"image_url\":\"data:image/png;base64,QUJD\",\"type\":\"input_image\"}]"}
	]}`)

	out, modified := normalizeBody(t, body)
	if !modified {
		t.Fatal("expected request to be modified")
	}
	output := decodedInput(t, out)[0].(map[string]any)["output"].([]any)
	part := output[0].(map[string]any)
	if part["type"] != "input_image" ||
		part["image_url"] != "data:image/png;base64,QUJD" ||
		part["detail"] != "original" {
		t.Fatalf("unexpected image part: %#v", part)
	}
}

func TestApplyImageCompatibilityRepairsNestedChatImage(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"function_call_output","call_id":"call-1","output":"[{\"type\":\"image_url\",\"image_url\":{\"url\":\"data:image/jpeg;base64,REVG\",\"detail\":\"high\"}}]"}
	]}`)

	out, modified := normalizeBody(t, body)
	if !modified {
		t.Fatal("expected request to be modified")
	}
	part := decodedInput(t, out)[0].(map[string]any)["output"].([]any)[0].(map[string]any)
	if part["type"] != "input_image" ||
		part["image_url"] != "data:image/jpeg;base64,REVG" ||
		part["detail"] != "high" {
		t.Fatalf("unexpected nested image part: %#v", part)
	}
}

func TestApplyImageCompatibilityPreservesMixedPartOrder(t *testing.T) {
	stringified, err := json.Marshal([]any{
		map[string]any{"type": "text", "text": "before"},
		map[string]any{"type": "input_image", "image_url": "data:image/png;base64,QUJD"},
		map[string]any{"type": "output_text", "text": "after"},
		map[string]any{"type": "image_url", "image_url": "data:image/png;base64,REVG"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"input": []any{
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call-1",
				"output":  string(stringified),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	out, modified := normalizeBody(t, body)
	if !modified {
		t.Fatal("expected request to be modified")
	}
	parts := decodedInput(t, out)[0].(map[string]any)["output"].([]any)
	if len(parts) != 4 {
		t.Fatalf("expected four parts, got %d", len(parts))
	}
	wantTypes := []string{"input_text", "input_image", "input_text", "input_image"}
	for index, want := range wantTypes {
		if got := parts[index].(map[string]any)["type"]; got != want {
			t.Fatalf("part %d type = %#v, want %q", index, got, want)
		}
	}
	if parts[0].(map[string]any)["text"] != "before" ||
		parts[2].(map[string]any)["text"] != "after" {
		t.Fatalf("text parts changed: %#v", parts)
	}
}

func TestApplyImageCompatibilityRepairsEveryToolOutput(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"function_call_output","call_id":"call-1","output":"[{\"type\":\"input_image\",\"image_url\":\"data:image/png;base64,QQ==\"}]"},
		{"type":"function_call_output","call_id":"call-2","output":"[{\"type\":\"input_image\",\"image_url\":\"data:image/png;base64,Qg==\"}]"},
		{"type":"function_call_output","call_id":"call-3","output":"[{\"type\":\"input_image\",\"image_url\":\"data:image/png;base64,Qw==\"}]"}
	]}`)

	out, modified := normalizeBody(t, body)
	if !modified {
		t.Fatal("expected request to be modified")
	}
	input := decodedInput(t, out)
	for index, want := range []string{
		"data:image/png;base64,QQ==",
		"data:image/png;base64,Qg==",
		"data:image/png;base64,Qw==",
	} {
		output := input[index].(map[string]any)["output"]
		parts, ok := output.([]any)
		if !ok || len(parts) != 1 {
			t.Fatalf("output %d remained stringified: %#v", index, output)
		}
		if got := parts[0].(map[string]any)["image_url"]; got != want {
			t.Fatalf("output %d image URL = %#v, want %q", index, got, want)
		}
	}
}

func TestApplyImageCompatibilityPreservesUnrecognizedParts(t *testing.T) {
	stringified := `[
		{"type":"input_image","image_url":"data:image/png;base64,QUJD"},
		{"type":"input_audio","audio":{"data":"QUJD","format":"wav"}},
		{"type":"custom_payload","payload":{"nested":[7,true]}},
		42
	]`
	body, err := json.Marshal(map[string]any{
		"input": []any{map[string]any{
			"type":    "function_call_output",
			"call_id": "call-1",
			"output":  stringified,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	out, modified := normalizeBody(t, body)
	if !modified {
		t.Fatal("expected request to be modified")
	}
	parts := decodedInput(t, out)[0].(map[string]any)["output"].([]any)
	var want []any
	if err := json.Unmarshal([]byte(stringified), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("parts were not preserved: got %#v, want %#v", parts, want)
	}
}

func TestApplyImageCompatibilityLeavesUnrelatedOutputAlone(t *testing.T) {
	tests := []string{
		`{"ok":true}`,
		`[{"type":"text","text":"ordinary tool output"}]`,
		`[{"type":"input_image","image_url":`,
		`[{"type":"input_image"}]`,
	}
	for _, output := range tests {
		body, err := json.Marshal(map[string]any{
			"input": []any{
				map[string]any{
					"type":   "function_call_output",
					"output": output,
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		out, modified := normalizeBody(t, body)
		if modified || out != nil {
			t.Fatalf("output should be unchanged: %q", output)
		}
	}
}

func TestApplyImageCompatibilityNeverRewritesStructuredImages(t *testing.T) {
	imageURL := "data:image/png;base64," + strings.Repeat("A", 4*1024*1024)
	body, err := json.Marshal(map[string]any{
		"input": []any{
			map[string]any{
				"type":    "function_call_output",
				"call_id": "call-large",
				"output": []any{
					map[string]any{
						"type":      "input_image",
						"image_url": imageURL,
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	out, modified := normalizeBody(t, body)
	if modified || out != nil {
		t.Fatal("already structured image was unexpectedly rewritten")
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
		out, modified, err := applyImageCompatibility(req, defaultCompatibilityConfig())
		if err != nil {
			t.Fatal(err)
		}
		if modified || out != nil {
			t.Fatalf("request should have bypassed plugin: %+v", req)
		}
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
	cfg := defaultCompatibilityConfig()
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
	cfg := defaultCompatibilityConfig()
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
