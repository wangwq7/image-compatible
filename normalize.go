package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type compatibilityConfig struct {
	Models        []string `yaml:"models"`
	SourceFormats []string `yaml:"source_formats"`
	TargetFormats []string `yaml:"target_formats"`
}

func defaultCompatibilityConfig() compatibilityConfig {
	return compatibilityConfig{
		Models:        []string{"gpt-5.6-sol", "p/gpt-5.6-sol"},
		SourceFormats: []string{"openai"},
		TargetFormats: []string{"codex"},
	}
}

func (cfg compatibilityConfig) clone() compatibilityConfig {
	cfg.Models = append([]string(nil), cfg.Models...)
	cfg.SourceFormats = append([]string(nil), cfg.SourceFormats...)
	cfg.TargetFormats = append([]string(nil), cfg.TargetFormats...)
	return cfg
}

func (cfg *compatibilityConfig) normalize() error {
	if len(cfg.Models) == 0 {
		return fmt.Errorf("models must not be empty")
	}
	if len(cfg.SourceFormats) == 0 {
		cfg.SourceFormats = []string{"openai"}
	}
	if len(cfg.TargetFormats) == 0 {
		cfg.TargetFormats = []string{"codex"}
	}
	return nil
}

func applyImageCompatibility(req requestTransformRequest, cfg compatibilityConfig) ([]byte, bool, error) {
	if !matchesAny(req.FromFormat, cfg.SourceFormats) ||
		!matchesAny(req.ToFormat, cfg.TargetFormats) ||
		!matchesModel(req.Model, cfg.Models) {
		return nil, false, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(req.Body))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, false, fmt.Errorf("decode request body: %w", err)
	}

	input, ok := root["input"].([]any)
	if !ok {
		return nil, false, nil
	}

	modified := false
	for _, rawItem := range input {
		item, okItem := rawItem.(map[string]any)
		if !okItem || stringValue(item["type"]) != "function_call_output" {
			continue
		}
		normalized, repaired := normalizeStringifiedToolOutput(item["output"])
		if !repaired {
			continue
		}
		item["output"] = normalized
		modified = true
	}

	if !modified {
		return nil, false, nil
	}
	body, err := json.Marshal(root)
	if err != nil {
		return nil, false, fmt.Errorf("encode request body: %w", err)
	}
	return body, true, nil
}

func normalizeStringifiedToolOutput(output any) ([]any, bool) {
	text, ok := output.(string)
	if !ok {
		return nil, false
	}
	var rawParts []any
	if err := json.Unmarshal([]byte(text), &rawParts); err != nil || !containsRecognizedImage(rawParts) {
		return nil, false
	}
	normalized := make([]any, 0, len(rawParts))
	for _, rawPart := range rawParts {
		normalized = append(normalized, normalizeToolOutputPart(rawPart))
	}
	return normalized, true
}

func containsRecognizedImage(parts []any) bool {
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}
		if _, recognized := normalizeImagePart(part); recognized {
			return true
		}
	}
	return false
}

func normalizeToolOutputPart(rawPart any) any {
	part, ok := rawPart.(map[string]any)
	if !ok {
		return fallbackTextPart(rawPart)
	}
	if image, recognized := normalizeImagePart(part); recognized {
		return image
	}

	switch stringValue(part["type"]) {
	case "text", "input_text", "output_text":
		return map[string]any{
			"type": "input_text",
			"text": stringValue(part["text"]),
		}
	case "file":
		file, _ := part["file"].(map[string]any)
		fileID := stringValue(file["file_id"])
		fileData := stringValue(file["file_data"])
		fileURL := stringValue(file["file_url"])
		if fileID == "" && fileData == "" && fileURL == "" {
			return fallbackTextPart(rawPart)
		}
		out := map[string]any{"type": "input_file"}
		if fileID != "" {
			out["file_id"] = fileID
		}
		if fileData != "" {
			out["file_data"] = fileData
		}
		if fileURL != "" {
			out["file_url"] = fileURL
		}
		if filename := stringValue(file["filename"]); filename != "" {
			out["filename"] = filename
		}
		return out
	default:
		return fallbackTextPart(rawPart)
	}
}

func normalizeImagePart(part map[string]any) (map[string]any, bool) {
	partType := stringValue(part["type"])
	if partType != "input_image" && partType != "image_url" {
		return nil, false
	}

	imageURL := ""
	fileID := ""
	detail := stringValue(part["detail"])
	if partType == "input_image" {
		imageURL = stringValue(part["image_url"])
		fileID = stringValue(part["file_id"])
	} else {
		switch image := part["image_url"].(type) {
		case map[string]any:
			imageURL = stringValue(image["url"])
			fileID = stringValue(image["file_id"])
			if detail == "" {
				detail = stringValue(image["detail"])
			}
		case string:
			imageURL = image
		}
		if imageURL == "" {
			imageURL = stringValue(part["url"])
		}
	}
	if imageURL == "" && fileID == "" {
		return nil, false
	}

	out := map[string]any{"type": "input_image"}
	if imageURL != "" {
		out["image_url"] = imageURL
	}
	if fileID != "" {
		out["file_id"] = fileID
	}
	if detail != "" {
		out["detail"] = detail
	}
	return out, true
}

func fallbackTextPart(value any) map[string]any {
	raw, err := json.Marshal(value)
	if err != nil {
		raw = []byte(fmt.Sprint(value))
	}
	return map[string]any{
		"type": "input_text",
		"text": string(raw),
	}
}

func matchesAny(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func matchesModel(model string, allowed []string) bool {
	model = strings.TrimSpace(model)
	for _, candidate := range allowed {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if strings.EqualFold(model, candidate) ||
			strings.EqualFold(strings.TrimPrefix(model, "p/"), strings.TrimPrefix(candidate, "p/")) {
			return true
		}
	}
	return false
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
