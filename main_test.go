package main

import (
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
}
