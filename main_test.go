package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPluginRegistrationMeetsHostRequirements(t *testing.T) {
	registration := pluginRegistration()
	metadata := registration.Metadata

	required := map[string]string{
		"name":              metadata.Name,
		"version":           metadata.Version,
		"author":            metadata.Author,
		"github_repository": metadata.GitHubRepository,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("%s must not be empty", field)
		}
	}
	if !registration.Capabilities.RequestNormalizer {
		t.Fatal("request_normalizer capability must be enabled")
	}
	if metadata.Version != pluginVersion {
		t.Fatalf("version = %q, want %q", metadata.Version, pluginVersion)
	}
	var fieldNames []string
	for _, field := range metadata.ConfigFields {
		fieldNames = append(fieldNames, field.Name)
	}
	wantFields := []string{"models", "source_formats", "target_formats"}
	if !reflect.DeepEqual(fieldNames, wantFields) {
		t.Fatalf("config fields = %#v, want %#v", fieldNames, wantFields)
	}
}

func TestConfigureCoreFields(t *testing.T) {
	defer func() {
		if err := configure(nil); err != nil {
			t.Fatal(err)
		}
	}()
	raw, err := json.Marshal(lifecycleRequest{
		ConfigYAML: []byte("models:\n  - gpt-5.6-sol\nsource_formats:\n  - openai\ntarget_formats:\n  - codex\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := configure(raw); err != nil {
		t.Fatal(err)
	}
	cfg := currentConfig()
	if !reflect.DeepEqual(cfg.Models, []string{"gpt-5.6-sol"}) {
		t.Fatalf("models = %#v", cfg.Models)
	}
	if !reflect.DeepEqual(cfg.SourceFormats, []string{"openai"}) {
		t.Fatalf("source formats = %#v", cfg.SourceFormats)
	}
	if !reflect.DeepEqual(cfg.TargetFormats, []string{"codex"}) {
		t.Fatalf("target formats = %#v", cfg.TargetFormats)
	}
}
