package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

const (
	defaultContextWindowTokens     = int64(272000)
	defaultReserveTokens           = int64(32000)
	defaultMaxBase64Bytes          = int64(24 * 1024 * 1024)
	defaultEstimatedTokensPerImage = int64(8192)
	defaultBytesPerTextToken       = 2.0
	defaultKeepRecentToolImages    = 2
	defaultScreenshotPlaceholder   = "[Earlier tool screenshot omitted by image-compatible]"
)

type guardConfig struct {
	Models                  []string `yaml:"models"`
	SourceFormats           []string `yaml:"source_formats"`
	TargetFormats           []string `yaml:"target_formats"`
	ToolNamePatterns        []string `yaml:"tool_name_patterns"`
	RepairStringifiedImages bool     `yaml:"repair_stringified_images"`
	TrimOldToolImages       bool     `yaml:"trim_old_tool_images"`
	ContextWindowTokens     int64    `yaml:"context_window_tokens"`
	ReserveTokens           int64    `yaml:"reserve_tokens"`
	MaxBase64Bytes          int64    `yaml:"max_base64_bytes"`
	EstimatedTokensPerImage int64    `yaml:"estimated_tokens_per_image"`
	BytesPerTextToken       float64  `yaml:"bytes_per_text_token"`
	KeepRecentToolImages    int      `yaml:"keep_recent_tool_images"`
	Placeholder             string   `yaml:"placeholder"`
}

type imageCandidate struct {
	parts []any
	index int
}

type imageStats struct {
	base64Bytes  int64
	dataURLChars int64
	imageCount   int64
}

func defaultGuardConfig() guardConfig {
	return guardConfig{
		Models:                  []string{"gpt-5.6-sol", "p/gpt-5.6-sol"},
		SourceFormats:           []string{"openai"},
		TargetFormats:           []string{"codex"},
		ToolNamePatterns:        []string{"screenshot", "view_image"},
		RepairStringifiedImages: true,
		TrimOldToolImages:       true,
		ContextWindowTokens:     defaultContextWindowTokens,
		ReserveTokens:           defaultReserveTokens,
		MaxBase64Bytes:          defaultMaxBase64Bytes,
		EstimatedTokensPerImage: defaultEstimatedTokensPerImage,
		BytesPerTextToken:       defaultBytesPerTextToken,
		KeepRecentToolImages:    defaultKeepRecentToolImages,
		Placeholder:             defaultScreenshotPlaceholder,
	}
}

func (cfg guardConfig) clone() guardConfig {
	cfg.Models = append([]string(nil), cfg.Models...)
	cfg.SourceFormats = append([]string(nil), cfg.SourceFormats...)
	cfg.TargetFormats = append([]string(nil), cfg.TargetFormats...)
	cfg.ToolNamePatterns = append([]string(nil), cfg.ToolNamePatterns...)
	return cfg
}

func (cfg *guardConfig) normalize() error {
	if len(cfg.Models) == 0 {
		return fmt.Errorf("models must not be empty")
	}
	if len(cfg.SourceFormats) == 0 {
		cfg.SourceFormats = []string{"openai"}
	}
	if len(cfg.TargetFormats) == 0 {
		cfg.TargetFormats = []string{"codex"}
	}
	if len(cfg.ToolNamePatterns) == 0 {
		cfg.ToolNamePatterns = []string{"screenshot", "view_image"}
	}
	if cfg.ContextWindowTokens <= 0 {
		return fmt.Errorf("context_window_tokens must be greater than zero")
	}
	if cfg.ReserveTokens < 0 || cfg.ReserveTokens >= cfg.ContextWindowTokens {
		return fmt.Errorf("reserve_tokens must be between zero and context_window_tokens")
	}
	if cfg.MaxBase64Bytes < 0 {
		return fmt.Errorf("max_base64_bytes must not be negative")
	}
	if cfg.EstimatedTokensPerImage < 0 {
		return fmt.Errorf("estimated_tokens_per_image must not be negative")
	}
	if cfg.BytesPerTextToken <= 0 {
		return fmt.Errorf("bytes_per_text_token must be greater than zero")
	}
	if cfg.KeepRecentToolImages < 0 {
		return fmt.Errorf("keep_recent_tool_images must not be negative")
	}
	cfg.Placeholder = strings.TrimSpace(cfg.Placeholder)
	if cfg.Placeholder == "" {
		cfg.Placeholder = defaultScreenshotPlaceholder
	}
	return nil
}

func applyImageCompatibility(req requestTransformRequest, cfg guardConfig) ([]byte, bool, error) {
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
	candidates := make([]imageCandidate, 0)
	toolNames := collectToolCallNames(input)
	for _, rawItem := range input {
		item, okItem := rawItem.(map[string]any)
		if !okItem || stringValue(item["type"]) != "function_call_output" {
			continue
		}

		output := item["output"]
		if cfg.RepairStringifiedImages {
			if normalized, repaired := normalizeStringifiedToolOutput(output); repaired {
				item["output"] = normalized
				output = normalized
				modified = true
			}
		}

		parts, okParts := output.([]any)
		if !okParts || !matchesToolName(toolNames[stringValue(item["call_id"])], cfg.ToolNamePatterns) {
			continue
		}
		for index, rawPart := range parts {
			part, okPart := rawPart.(map[string]any)
			if !okPart {
				continue
			}
			if imageDataURL(part) != "" {
				candidates = append(candidates, imageCandidate{
					parts: parts,
					index: index,
				})
			}
		}
	}

	if cfg.TrimOldToolImages && len(candidates) > 0 {
		stats, estimatedTokens, err := requestEstimate(root, cfg)
		if err != nil {
			return nil, false, err
		}
		inputLimit := cfg.ContextWindowTokens - cfg.ReserveTokens

		trimmed := make([]bool, len(candidates))
		for pass := 0; pass < 2 && limitsExceeded(stats, estimatedTokens, inputLimit, cfg); pass++ {
			for index, candidate := range candidates {
				if trimmed[index] {
					continue
				}
				remaining := len(candidates) - countTrimmed(trimmed)
				if pass == 0 && remaining <= cfg.KeepRecentToolImages {
					continue
				}
				candidate.parts[candidate.index] = map[string]any{
					"type": "input_text",
					"text": cfg.Placeholder,
				}
				trimmed[index] = true
				stats, estimatedTokens, err = requestEstimate(root, cfg)
				if err != nil {
					return nil, false, err
				}
				modified = true
				if !limitsExceeded(stats, estimatedTokens, inputLimit, cfg) {
					break
				}
			}
		}
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

func countTrimmed(trimmed []bool) int {
	count := 0
	for _, value := range trimmed {
		if value {
			count++
		}
	}
	return count
}

func collectToolCallNames(input []any) map[string]string {
	names := make(map[string]string)
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok || stringValue(item["type"]) != "function_call" {
			continue
		}
		callID := stringValue(item["call_id"])
		if callID == "" {
			continue
		}
		names[callID] = stringValue(item["name"])
	}
	return names
}

func matchesToolName(name string, patterns []string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern != "" && strings.Contains(name, pattern) {
			return true
		}
	}
	return false
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
		switch stringValue(part["type"]) {
		case "image_url", "input_image":
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
	switch stringValue(part["type"]) {
	case "text", "input_text", "output_text":
		return map[string]any{
			"type": "input_text",
			"text": stringValue(part["text"]),
		}
	case "input_image":
		imageURL := stringValue(part["image_url"])
		fileID := stringValue(part["file_id"])
		if imageURL == "" && fileID == "" {
			return fallbackTextPart(rawPart)
		}
		out := map[string]any{"type": "input_image"}
		if imageURL != "" {
			out["image_url"] = imageURL
		}
		if fileID != "" {
			out["file_id"] = fileID
		}
		if detail := stringValue(part["detail"]); detail != "" {
			out["detail"] = detail
		}
		return out
	case "image_url":
		image, _ := part["image_url"].(map[string]any)
		imageURL := stringValue(image["url"])
		fileID := stringValue(image["file_id"])
		if imageURL == "" && fileID == "" {
			return fallbackTextPart(rawPart)
		}
		out := map[string]any{"type": "input_image"}
		if imageURL != "" {
			out["image_url"] = imageURL
		}
		if fileID != "" {
			out["file_id"] = fileID
		}
		if detail := stringValue(image["detail"]); detail != "" {
			out["detail"] = detail
		}
		return out
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

func imageDataURL(part map[string]any) string {
	if stringValue(part["type"]) != "input_image" {
		return ""
	}
	value := stringValue(part["image_url"])
	if !strings.HasPrefix(strings.ToLower(value), "data:image/") {
		return ""
	}
	if !strings.Contains(value, ";base64,") {
		return ""
	}
	return value
}

func decodedBase64Size(dataURL string) int64 {
	comma := strings.IndexByte(dataURL, ',')
	if comma < 0 || comma == len(dataURL)-1 {
		return 0
	}
	payload := dataURL[comma+1:]
	padding := int64(0)
	if strings.HasSuffix(payload, "==") {
		padding = 2
	} else if strings.HasSuffix(payload, "=") {
		padding = 1
	}
	size := int64(len(payload))*3/4 - padding
	if size < 0 {
		return 0
	}
	return size
}

func collectImageStats(value any) imageStats {
	var stats imageStats
	walkJSON(value, func(part map[string]any) {
		if dataURL := imageDataURL(part); dataURL != "" {
			stats.base64Bytes += decodedBase64Size(dataURL)
			stats.dataURLChars += int64(len(dataURL))
			stats.imageCount++
		}
	})
	return stats
}

func walkJSON(value any, visit func(map[string]any)) {
	switch typed := value.(type) {
	case map[string]any:
		visit(typed)
		for _, child := range typed {
			walkJSON(child, visit)
		}
	case []any:
		for _, child := range typed {
			walkJSON(child, visit)
		}
	}
}

func estimateInputTokens(bodyBytes int64, stats imageStats, cfg guardConfig) int64 {
	textBytes := bodyBytes - stats.dataURLChars
	if textBytes < 0 {
		textBytes = 0
	}
	textTokens := int64(math.Ceil(float64(textBytes) / cfg.BytesPerTextToken))
	return textTokens + stats.imageCount*cfg.EstimatedTokensPerImage
}

func requestEstimate(root map[string]any, cfg guardConfig) (imageStats, int64, error) {
	body, err := json.Marshal(root)
	if err != nil {
		return imageStats{}, 0, fmt.Errorf("encode request for estimate: %w", err)
	}
	stats := collectImageStats(root)
	return stats, estimateInputTokens(int64(len(body)), stats, cfg), nil
}

func limitsExceeded(stats imageStats, estimatedTokens, inputLimit int64, cfg guardConfig) bool {
	base64Exceeded := cfg.MaxBase64Bytes > 0 && stats.base64Bytes > cfg.MaxBase64Bytes
	tokenExceeded := inputLimit > 0 && estimatedTokens > inputLimit
	return base64Exceeded || tokenExceeded
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
