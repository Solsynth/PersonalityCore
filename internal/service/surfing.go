package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

const (
	listFeedToolName          = "list_feed"
	searchPostsToolName       = "search_posts"
	createPostToolName        = "create_post"
	replyToPostToolName       = "reply_to_post"
	repostPostToolName        = "repost_post"
	reactToPostToolName       = "react_to_post"
	getPostSurfingToolName    = "get_post"
	getPostRepliesToolName    = "get_post_replies"
	listMyPostsToolName       = "list_my_posts"
)

// --- Tool info ---

func (s *ConversationService) listFeedToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: listFeedToolName,
		Desc: "Browse recent posts from the Solar Network feed. Use shuffle=true for random content.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"offset":  {Type: schema.Integer, Desc: "Pagination offset (default 0)."},
			"take":    {Type: schema.Integer, Desc: "Number of posts to return (default 20, max 50)."},
			"shuffle": {Type: schema.Boolean, Desc: "Randomize the order of posts."},
		}),
	}
}

func (s *ConversationService) searchPostsToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: searchPostsToolName,
		Desc: "Search for posts on Solar Network by keyword.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query":  {Type: schema.String, Desc: "Search query.", Required: true},
			"offset": {Type: schema.Integer, Desc: "Pagination offset (default 0)."},
			"take":   {Type: schema.Integer, Desc: "Number of results (default 20)."},
		}),
	}
}

func (s *ConversationService) createPostToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: createPostToolName,
		Desc: "Create and publish a new post on Solar Network.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"content": {Type: schema.String, Desc: "The post content.", Required: true},
			"title":   {Type: schema.String, Desc: "Optional title."},
			"tags":    {Type: schema.String, Desc: "Comma-separated tags."},
		}),
	}
}

func (s *ConversationService) replyToPostToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: replyToPostToolName,
		Desc: "Reply to an existing post on Solar Network.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"post_id": {Type: schema.String, Desc: "The ID of the post to reply to.", Required: true},
			"content": {Type: schema.String, Desc: "The reply content.", Required: true},
		}),
	}
}

func (s *ConversationService) repostPostToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: repostPostToolName,
		Desc: "Repost (share) a post on Solar Network, optionally with a comment.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"post_id": {Type: schema.String, Desc: "The ID of the post to repost.", Required: true},
			"comment": {Type: schema.String, Desc: "Optional comment to add."},
		}),
	}
}

func (s *ConversationService) reactToPostToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: reactToPostToolName,
		Desc: "React to a post with an emoji. Valid symbols: thumb_up, heart, clap, laugh, party, pray, cry, confuse, angry, just_okay, thumb_down.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"post_id":  {Type: schema.String, Desc: "The post ID to react to.", Required: true},
			"symbol":   {Type: schema.String, Desc: "Reaction symbol. Default thumb_up."},
			"attitude": {Type: schema.String, Desc: "Positive, Neutral, or Negative. Default Positive."},
		}),
	}
}

func (s *ConversationService) getPostSurfingToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: getPostSurfingToolName,
		Desc: "Get a single post by ID.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"post_id": {Type: schema.String, Desc: "The post ID.", Required: true},
		}),
	}
}

func (s *ConversationService) getPostRepliesSurfingToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: getPostRepliesToolName,
		Desc: "Get replies to a post.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"post_id": {Type: schema.String, Desc: "The post ID.", Required: true},
			"offset":  {Type: schema.Integer, Desc: "Pagination offset."},
			"take":    {Type: schema.Integer, Desc: "Number of replies."},
		}),
	}
}

func (s *ConversationService) listMyPostsToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: listMyPostsToolName,
		Desc: "List posts published by your publisher account.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"offset": {Type: schema.Integer, Desc: "Pagination offset."},
			"take":   {Type: schema.Integer, Desc: "Number of posts."},
		}),
	}
}

// --- Dispatch ---

func isSurfingToolName(name string) bool {
	switch name {
	case listFeedToolName, searchPostsToolName, createPostToolName,
		replyToPostToolName, repostPostToolName, reactToPostToolName,
		getPostSurfingToolName, getPostRepliesToolName, listMyPostsToolName:
		return true
	}
	return false
}

func (s *ConversationService) executeSurfingToolCall(ctx context.Context, agentID, accountID string, call schema.ToolCall) (*executedChatToolResult, error) {
	if s.sn == nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": "solar network bridge not configured"})
	}

	switch call.Function.Name {
	case listFeedToolName:
		return s.executeListFeedToolCall(ctx, agentID, call)
	case searchPostsToolName:
		return s.executeSearchPostsToolCall(ctx, agentID, call)
	case createPostToolName:
		return s.executeCreatePostToolCall(ctx, agentID, call)
	case replyToPostToolName:
		return s.executeReplyToPostToolCall(ctx, agentID, call)
	case repostPostToolName:
		return s.executeRepostPostToolCall(ctx, agentID, call)
	case reactToPostToolName:
		return s.executeReactToPostToolCall(ctx, agentID, call)
	case getPostSurfingToolName:
		return s.executeGetPostSurfingToolCall(ctx, agentID, call)
	case getPostRepliesToolName:
		return s.executeGetPostRepliesToolCall(ctx, agentID, call)
	case listMyPostsToolName:
		return s.executeListMyPostsToolCall(ctx, agentID, call)
	default:
		return nil, fmt.Errorf("unsupported surfing tool %q", call.Function.Name)
	}
}

// --- Tool execution ---

type listFeedInput struct {
	Offset  int  `json:"offset"`
	Take    int  `json:"take"`
	Shuffle bool `json:"shuffle"`
}

type searchPostsInput struct {
	Query  string `json:"query"`
	Offset int    `json:"offset"`
	Take   int    `json:"take"`
}

type createPostInput struct {
	Content string `json:"content"`
	Title   string `json:"title"`
	Tags    string `json:"tags"`
}

type replyToPostInput struct {
	PostID  string `json:"post_id"`
	Content string `json:"content"`
}

type repostPostInput struct {
	PostID  string  `json:"post_id"`
	Comment *string `json:"comment"`
}

type reactToPostInput struct {
	PostID   string `json:"post_id"`
	Symbol   string `json:"symbol"`
	Attitude string `json:"attitude"`
}

type getPostInput struct {
	PostID string `json:"post_id"`
}

type getPostRepliesInput struct {
	PostID string `json:"post_id"`
	Offset int    `json:"offset"`
	Take   int    `json:"take"`
}

type listMyPostsInput struct {
	Offset int `json:"offset"`
	Take   int `json:"take"`
}

func (s *ConversationService) executeListFeedToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input listFeedInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return nil, fmt.Errorf("decode %s: %w", listFeedToolName, err)
	}
	take := input.Take
	if take <= 0 || take > 50 {
		take = 20
	}
	feed, err := s.sn.ListFeed(ctx, agentID, input.Offset, take, input.Shuffle)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "total": feed.Total, "posts": feed.Items})
}

func (s *ConversationService) executeSearchPostsToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input searchPostsInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return nil, fmt.Errorf("decode %s: %w", searchPostsToolName, err)
	}
	if strings.TrimSpace(input.Query) == "" {
		return toolResultJSON(call, map[string]any{"ok": false, "error": "query is required"})
	}
	take := input.Take
	if take <= 0 || take > 50 {
		take = 20
	}
	results, err := s.sn.SearchPosts(ctx, agentID, input.Query, input.Offset, take)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "total": results.Total, "posts": results.Items})
}

func (s *ConversationService) resolvePublisherName(ctx context.Context, agentID string) string {
	def, ok := s.GetAgent(agentID)
	if !ok {
		return ""
	}
	if def.SolarIntegration.PublisherName != "" {
		return def.SolarIntegration.PublisherName
	}
	return def.SolarIntegration.AccountName
}

func (s *ConversationService) executeCreatePostToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input createPostInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return nil, fmt.Errorf("decode %s: %w", createPostToolName, err)
	}
	if strings.TrimSpace(input.Content) == "" {
		return toolResultJSON(call, map[string]any{"ok": false, "error": "content is required"})
	}

	body := map[string]any{
		"content": strings.TrimSpace(input.Content),
	}
	if strings.TrimSpace(input.Title) != "" {
		body["title"] = strings.TrimSpace(input.Title)
	}
	if strings.TrimSpace(input.Tags) != "" {
		tags := strings.Split(strings.TrimSpace(input.Tags), ",")
		for i := range tags {
			tags[i] = strings.TrimSpace(tags[i])
		}
		body["tags"] = tags
	}

	pubName := s.resolvePublisherName(ctx, agentID)
	post, err := s.sn.CreatePost(ctx, agentID, pubName, body)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "post": post})
}

func (s *ConversationService) executeReplyToPostToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input replyToPostInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return nil, fmt.Errorf("decode %s: %w", replyToPostToolName, err)
	}
	if strings.TrimSpace(input.PostID) == "" || strings.TrimSpace(input.Content) == "" {
		return toolResultJSON(call, map[string]any{"ok": false, "error": "post_id and content are required"})
	}

	pubName := s.resolvePublisherName(ctx, agentID)
	post, err := s.sn.ReplyToPost(ctx, agentID, pubName, input.PostID, input.Content)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "post": post})
}

func (s *ConversationService) executeRepostPostToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input repostPostInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return nil, fmt.Errorf("decode %s: %w", repostPostToolName, err)
	}
	if strings.TrimSpace(input.PostID) == "" {
		return toolResultJSON(call, map[string]any{"ok": false, "error": "post_id is required"})
	}

	pubName := s.resolvePublisherName(ctx, agentID)
	post, err := s.sn.RepostPost(ctx, agentID, pubName, input.PostID, input.Comment)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "post": post})
}

var validReactionSymbols = map[string]bool{
	"thumb_up": true, "thumb_down": true, "just_okay": true,
	"cry": true, "confuse": true, "clap": true, "laugh": true,
	"angry": true, "party": true, "pray": true, "heart": true,
}

func (s *ConversationService) executeReactToPostToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input reactToPostInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return nil, fmt.Errorf("decode %s: %w", reactToPostToolName, err)
	}
	if strings.TrimSpace(input.PostID) == "" {
		return toolResultJSON(call, map[string]any{"ok": false, "error": "post_id is required"})
	}
	symbol := strings.TrimSpace(input.Symbol)
	if symbol == "" {
		symbol = "thumb_up"
	}
	if !validReactionSymbols[symbol] {
		return toolResultJSON(call, map[string]any{"ok": false, "error": "invalid symbol, valid: thumb_up, heart, clap, laugh, party, pray, cry, confuse, angry, just_okay, thumb_down"})
	}
	attitude := 0 // Positive
	switch strings.ToLower(strings.TrimSpace(input.Attitude)) {
	case "negative":
		attitude = 2
	case "neutral":
		attitude = 1
	}

	if err := s.sn.ReactToPost(ctx, agentID, input.PostID, symbol, attitude); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "reacted": symbol})
}

func (s *ConversationService) executeGetPostSurfingToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input getPostInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return nil, fmt.Errorf("decode %s: %w", getPostSurfingToolName, err)
	}
	if strings.TrimSpace(input.PostID) == "" {
		return toolResultJSON(call, map[string]any{"ok": false, "error": "post_id is required"})
	}

	post, err := s.sn.GetPost(ctx, agentID, input.PostID)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "post": post})
}

func (s *ConversationService) executeGetPostRepliesToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input getPostRepliesInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return nil, fmt.Errorf("decode %s: %w", getPostRepliesToolName, err)
	}
	if strings.TrimSpace(input.PostID) == "" {
		return toolResultJSON(call, map[string]any{"ok": false, "error": "post_id is required"})
	}

	replies, err := s.sn.ListPostReplies(ctx, agentID, input.PostID, input.Offset, input.Take)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "total": replies.Total, "replies": replies.Items})
}

func (s *ConversationService) executeListMyPostsToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	var input listMyPostsInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return nil, fmt.Errorf("decode %s: %w", listMyPostsToolName, err)
	}

	pubName := s.resolvePublisherName(ctx, agentID)
	if pubName == "" {
		return toolResultJSON(call, map[string]any{"ok": false, "error": "no publisher configured for this agent"})
	}

	posts, err := s.sn.ListPublisherPosts(ctx, agentID, pubName, input.Offset, input.Take)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "total": posts.Total, "posts": posts.Items})
}
