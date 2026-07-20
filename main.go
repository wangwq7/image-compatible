package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	void* call;
	void* free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"sync"
	"unsafe"

	"gopkg.in/yaml.v3"
)

const (
	abiVersion              uint32 = 1
	schemaVersion           uint32 = 1
	methodPluginRegister           = "plugin.register"
	methodPluginReconfigure        = "plugin.reconfigure"
	methodRequestNormalize         = "request.normalize"
)

var runtimeConfig = struct {
	sync.RWMutex
	value guardConfig
}{
	value: defaultGuardConfig(),
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginMetadata         `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	RequestNormalizer bool `json:"request_normalizer"`
}

type pluginMetadata struct {
	Name             string
	Version          string
	Author           string
	GitHubRepository string
	Logo             string
	ConfigFields     []configField
}

type configField struct {
	Name        string
	Type        string
	EnumValues  []string
	Description string
}

type requestTransformRequest struct {
	FromFormat string
	ToFormat   string
	Model      string
	Stream     bool
	Body       []byte
}

type payloadResponse struct {
	Body []byte
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(abiVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}

	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handleMethod(C.GoString(method), requestBytes)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case methodPluginRegister, methodPluginReconfigure:
		if err := configure(request); err != nil {
			return nil, err
		}
		return okEnvelope(pluginRegistration())
	case methodRequestNormalize:
		return normalizeRequest(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	cfg := defaultGuardConfig()
	if len(raw) > 0 {
		var req lifecycleRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return fmt.Errorf("decode lifecycle request: %w", err)
		}
		if len(req.ConfigYAML) > 0 {
			if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
				return fmt.Errorf("decode plugin config: %w", err)
			}
		}
	}
	if err := cfg.normalize(); err != nil {
		return err
	}
	runtimeConfig.Lock()
	runtimeConfig.value = cfg
	runtimeConfig.Unlock()
	return nil
}

func currentConfig() guardConfig {
	runtimeConfig.RLock()
	defer runtimeConfig.RUnlock()
	return runtimeConfig.value.clone()
}

func normalizeRequest(raw []byte) ([]byte, error) {
	var req requestTransformRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode normalize request: %w", err)
	}
	body, modified, err := applyImageCompatibility(req, currentConfig())
	if err != nil {
		return nil, err
	}
	if !modified {
		return okEnvelope(payloadResponse{})
	}
	return okEnvelope(payloadResponse{Body: body})
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: schemaVersion,
		Metadata: pluginMetadata{
			Name:             "image-compatible",
			Version:          "0.1.0",
			Author:           "wangwq7",
			GitHubRepository: "https://github.com/wangwq7/image-compatible",
			Logo:             "",
			ConfigFields: []configField{
				{Name: "models", Type: "array", Description: "Models protected by the image compatibility normalizer."},
				{Name: "source_formats", Type: "array", Description: "Client formats eligible for normalization."},
				{Name: "target_formats", Type: "array", Description: "Upstream formats eligible for normalization."},
				{Name: "tool_name_patterns", Type: "array", Description: "Case-insensitive tool-name substrings eligible for old screenshot trimming."},
				{Name: "repair_stringified_images", Type: "boolean", Description: "Converts stringified tool image arrays into structured Codex image parts."},
				{Name: "trim_old_tool_images", Type: "boolean", Description: "Replaces the oldest tool screenshots when configured limits are exceeded."},
				{Name: "context_window_tokens", Type: "integer", Description: "Configured model context window used by the conservative estimator."},
				{Name: "reserve_tokens", Type: "integer", Description: "Tokens reserved for model output and estimation error."},
				{Name: "max_base64_bytes", Type: "integer", Description: "Maximum decoded base64 bytes retained across images before trimming old tool screenshots."},
				{Name: "estimated_tokens_per_image", Type: "integer", Description: "Conservative token estimate assigned to each structured image."},
				{Name: "bytes_per_text_token", Type: "number", Description: "Conservative UTF-8 byte-to-token ratio for non-image request content."},
				{Name: "keep_recent_tool_images", Type: "integer", Description: "Minimum number of most recent tool screenshots that are never trimmed."},
				{Name: "placeholder", Type: "string", Description: "Text inserted where an old tool screenshot was removed."},
			},
		},
		Capabilities: registrationCapability{RequestNormalizer: true},
	}
}

func okEnvelope(value any) ([]byte, error) {
	result, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: result})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{
		OK:    false,
		Error: &envelopeError{Code: code, Message: message},
	})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
