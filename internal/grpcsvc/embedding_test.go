package grpcsvc

import (
	"context"
	"testing"

	"google.golang.org/grpc/metadata"
)

func TestEmbeddingSettingsFromContext(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"x-embedding-model", "openai/text-embedding-3-small",
		"x-embedding-dimensions", "256",
	))

	settings, err := embeddingSettingsFromContext(ctx)
	if err != nil {
		t.Fatalf("embeddingSettingsFromContext() error = %v", err)
	}
	if settings.Model != "openai/text-embedding-3-small" {
		t.Fatalf("unexpected model %q", settings.Model)
	}
	if settings.Dimensions == nil || *settings.Dimensions != 256 {
		t.Fatalf("unexpected dimensions %#v", settings.Dimensions)
	}
}

func TestEmbeddingSettingsFromContextRejectsBadDimensions(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-embedding-dimensions", "abc"))
	if _, err := embeddingSettingsFromContext(ctx); err == nil {
		t.Fatal("expected invalid dimensions error")
	}
}
