package grpcsvc

import (
	"context"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	gen "src.solsynth.dev/sosys/go/proto"
	"src.solsynth.dev/sosys/personality/internal/service"
)

type EmbeddingService struct {
	gen.UnimplementedDyEmbeddingServiceServer

	conversations *service.ConversationService
}

type embeddingSettings struct {
	Model      string
	Dimensions *int
}

func NewEmbedding(conversations *service.ConversationService) *EmbeddingService {
	return &EmbeddingService{conversations: conversations}
}

func (s *EmbeddingService) GenerateEmbedding(ctx context.Context, req *gen.DyGenerateEmbeddingRequest) (*gen.DyGenerateEmbeddingResponse, error) {
	text := strings.TrimSpace(req.GetText())
	if text == "" {
		return nil, status.Error(codes.InvalidArgument, "text is required")
	}
	settings, err := embeddingSettingsFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.conversations.GenerateEmbeddings(ctx, service.GenerateEmbeddingsInput{
		Model:      settings.Model,
		Texts:      []string{text},
		Dimensions: settings.Dimensions,
	})
	if err != nil {
		return nil, mapError(err)
	}
	if len(result.Embeddings) == 0 {
		return &gen.DyGenerateEmbeddingResponse{}, nil
	}
	return &gen.DyGenerateEmbeddingResponse{
		Embedding:  append([]float32(nil), result.Embeddings[0]...),
		Dimensions: int32(len(result.Embeddings[0])),
	}, nil
}

func (s *EmbeddingService) GenerateEmbeddings(ctx context.Context, req *gen.DyGenerateEmbeddingsRequest) (*gen.DyGenerateEmbeddingsResponse, error) {
	texts := make([]string, 0, len(req.GetTexts()))
	for _, text := range req.GetTexts() {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, status.Error(codes.InvalidArgument, "texts must not contain empty values")
		}
		texts = append(texts, text)
	}
	if len(texts) == 0 {
		return nil, status.Error(codes.InvalidArgument, "texts is required")
	}
	settings, err := embeddingSettingsFromContext(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.conversations.GenerateEmbeddings(ctx, service.GenerateEmbeddingsInput{
		Model:      settings.Model,
		Texts:      texts,
		Dimensions: settings.Dimensions,
	})
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]*gen.DyEmbeddingItem, 0, len(result.Embeddings))
	for _, vector := range result.Embeddings {
		items = append(items, &gen.DyEmbeddingItem{
			Embedding:  append([]float32(nil), vector...),
			Dimensions: int32(len(vector)),
		})
	}
	return &gen.DyGenerateEmbeddingsResponse{Embeddings: items}, nil
}

func embeddingSettingsFromContext(ctx context.Context) (embeddingSettings, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return embeddingSettings{}, nil
	}
	settings := embeddingSettings{Model: firstMetadata(md, "x-embedding-model", "embedding-model")}
	dimensionsRaw := firstMetadata(md, "x-embedding-dimensions", "embedding-dimensions")
	if strings.TrimSpace(dimensionsRaw) == "" {
		return settings, nil
	}
	dimensions, err := strconv.Atoi(strings.TrimSpace(dimensionsRaw))
	if err != nil || dimensions < 0 {
		return embeddingSettings{}, status.Error(codes.InvalidArgument, "invalid embedding dimensions metadata")
	}
	settings.Dimensions = &dimensions
	return settings, nil
}

func firstMetadata(md metadata.MD, keys ...string) string {
	for _, key := range keys {
		values := md.Get(key)
		if len(values) > 0 && strings.TrimSpace(values[0]) != "" {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}
