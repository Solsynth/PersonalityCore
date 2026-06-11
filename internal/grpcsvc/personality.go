package grpcsvc

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gen "src.solsynth.dev/sosys/go/proto"
	"src.solsynth.dev/sosys/personality/internal/service"
)

type PersonalityService struct {
	gen.UnimplementedDyPersonalityServiceServer

	conversations *service.ConversationService
}

func Register(server interface {
	gen.DyPersonalityServiceServer
}, conversations *service.ConversationService) *PersonalityService {
	return &PersonalityService{conversations: conversations}
}

func New(conversations *service.ConversationService) *PersonalityService {
	return &PersonalityService{conversations: conversations}
}

func (s *PersonalityService) ListAgents(context.Context, *gen.DyListPersonalityAgentsRequest) (*gen.DyListPersonalityAgentsResponse, error) {
	items := s.conversations.ListAgents()
	out := make([]*gen.DyPersonalityAgent, 0, len(items))
	for _, item := range items {
		out = append(out, &gen.DyPersonalityAgent{
			Id:          item.ID,
			Name:        item.Name,
			Description: item.Description,
			Model:       item.Model,
			Abilities:   append([]string(nil), item.Abilities...),
		})
	}
	return &gen.DyListPersonalityAgentsResponse{Items: out}, nil
}

func (s *PersonalityService) GetAgent(_ context.Context, req *gen.DyGetPersonalityAgentRequest) (*gen.DyPersonalityAgent, error) {
	item, ok := s.conversations.GetAgent(req.GetId())
	if !ok {
		return nil, status.Error(codes.NotFound, "agent not found")
	}
	return &gen.DyPersonalityAgent{
		Id:          item.ID,
		Name:        item.Name,
		Description: item.Description,
		Model:       item.Model,
		Abilities:   append([]string(nil), item.Abilities...),
	}, nil
}

func (s *PersonalityService) RunConversation(ctx context.Context, req *gen.DyRunPersonalityConversationRequest) (*gen.DyRunPersonalityConversationResponse, error) {
	accountID := strings.TrimSpace(req.GetAccountId())
	if accountID == "" {
		return nil, status.Error(codes.InvalidArgument, "account_id is required")
	}
	message := strings.TrimSpace(req.GetMessage())
	if message == "" {
		return nil, status.Error(codes.InvalidArgument, "message is required")
	}

	threadID := strings.TrimSpace(req.GetConversationId())
	if threadID == "" {
		agentID := strings.TrimSpace(req.GetAgentId())
		if agentID == "" {
			return nil, status.Error(codes.InvalidArgument, "agent_id is required when conversation_id is empty")
		}
		thread, err := s.conversations.CreateConversation(ctx, accountID, service.CreateConversationInput{
			AgentID: agentID,
			Title:   "",
		})
		if err != nil {
			return nil, mapError(err)
		}
		threadID = thread.ID
	}

	result, err := s.conversations.ExecuteRun(ctx, accountID, threadID, service.RunInput{
		Message: message,
		Stream:  false,
	})
	if err != nil {
		return nil, mapError(err)
	}

	messageID := ""
	if result.ResponseMessage != nil {
		messageID = result.ResponseMessage.ID
	}

	return &gen.DyRunPersonalityConversationResponse{
		ConversationId: result.Thread.ID,
		RunId:          result.Run.ID,
		MessageId:      messageID,
		Content:        result.ResponseContent,
		Model:          result.Run.Model,
	}, nil
}

func mapError(err error) error {
	switch err {
	case service.ErrNotFound:
		return status.Error(codes.NotFound, err.Error())
	case service.ErrForbidden:
		return status.Error(codes.PermissionDenied, err.Error())
	default:
		return status.Error(codes.InvalidArgument, err.Error())
	}
}
