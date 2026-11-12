package llmtest

import (
	"context"
	"testing"

	"hornedviper/internal/config"
)

func TestProviderRequiresKey(t *testing.T) {
	_, err := Provider(context.Background(), config.Provider{Name: "x", Format: "openai", Model: "m"})
	if err == nil {
		t.Fatal("expected missing key error")
	}
}

func TestProviderRejectsUnknownFormat(t *testing.T) {
	_, err := Provider(context.Background(), config.Provider{
		Name: "x", Format: "carrier-pigeon", Model: "m", APIKey: "k",
	})
	if err == nil {
		t.Fatal("expected unknown format error")
	}
}
