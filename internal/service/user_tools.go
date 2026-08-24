package service

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/oklog/ulid/v2"

	"src.solsynth.dev/sosys/personality/internal/solar_network"
)

// ─── Tool name constants ───────────────────────────────────────────

const (
	listFilesToolName             = "list_files"
	getFileInfoToolName           = "get_file_info"
	createFolderToolName          = "create_folder"
	uploadTextFileToolName        = "upload_text_file"
	recycleFileToolName           = "recycle_file"
	restoreFileToolName           = "restore_file"
	listRecycleBinToolName        = "list_recycle_bin"
	getStorageQuotaToolName       = "get_storage_quota"
	listWalletsToolName           = "list_wallets"
	listOrdersToolName            = "list_orders"
	listNotificationsToolName     = "list_notifications"
	getUnreadNotificationCountToolName = "get_unread_notification_count"
	markAllNotificationsReadToolName   = "mark_all_notifications_read"
	readWebpageToolName           = "read_webpage"
	listStickersToolName          = "list_stickers"
	listSurveysToolName           = "list_surveys"
	getMyLevelingToolName         = "get_my_leveling"
	listRelationshipsToolName     = "list_relationships"
	followAccountToolName         = "follow_account"
	unfollowAccountToolName       = "unfollow_account"
	searchAccountsToolName        = "search_accounts"
)

// ─── Input structs ─────────────────────────────────────────────────

type listFilesInput struct {
	ParentID string `json:"parent_id,omitempty"`
	IsFolder *bool  `json:"is_folder,omitempty"`
	Name     string `json:"name,omitempty"`
	Take     *int   `json:"take,omitempty"`
	Offset   *int   `json:"offset,omitempty"`
}

type getFileInfoInput struct {
	FileID string `json:"file_id"`
}

type createFolderInput struct {
	Name     string  `json:"name"`
	ParentID *string `json:"parent_id,omitempty"`
}

type uploadTextFileInput struct {
	Name     string  `json:"name"`
	ParentID *string `json:"parent_id,omitempty"`
	Content  string  `json:"content"`
}

type recycleFileInput struct {
	FileID string `json:"file_id"`
}

type restoreFileInput struct {
	FileID string `json:"file_id"`
}

type listRecycleBinInput struct {
	Take   *int `json:"take,omitempty"`
	Offset *int `json:"offset,omitempty"`
}

type listOrdersInput struct {
	Take   *int `json:"take,omitempty"`
	Offset *int `json:"offset,omitempty"`
}

type listNotificationsInput struct {
	Take   *int `json:"take,omitempty"`
	Offset *int `json:"offset,omitempty"`
}

type readWebpageInput struct {
	URL string `json:"url"`
}

type listStickersInput struct {
	Take   *int `json:"take,omitempty"`
	Offset *int `json:"offset,omitempty"`
}

type listSurveysInput struct {
	Take   *int `json:"take,omitempty"`
	Offset *int `json:"offset,omitempty"`
}

type followAccountInput struct {
	AccountID string `json:"account_id"`
}

type unfollowAccountInput struct {
	AccountID string `json:"account_id"`
}

type searchAccountsInput struct {
	Query  string `json:"query"`
	Take   *int   `json:"take,omitempty"`
	Offset *int   `json:"offset,omitempty"`
}

// ─── Tool info builders ────────────────────────────────────────────

func (s *ConversationService) listFilesToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: listFilesToolName,
		Desc: "List files in your Solar Drive. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func (s *ConversationService) getFileInfoToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: getFileInfoToolName,
		Desc: "Get detailed information about a file in your Solar Drive. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"file_id": {Type: schema.String, Desc: "The file ID", Required: true},
		}),
	}
}

func (s *ConversationService) createFolderToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: createFolderToolName,
		Desc: "Create a folder in your Solar Drive. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name":      {Type: schema.String, Desc: "Folder name", Required: true},
			"parent_id": {Type: schema.String, Desc: "Parent folder ID (omit for root)"},
		}),
	}
}

func (s *ConversationService) uploadTextFileToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: uploadTextFileToolName,
		Desc: "Upload a text file to your Solar Drive. Content should be the file body text. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name":      {Type: schema.String, Desc: "File name", Required: true},
			"parent_id": {Type: schema.String, Desc: "Parent folder ID (omit for root)"},
			"content":   {Type: schema.String, Desc: "File content as text", Required: true},
		}),
	}
}

func (s *ConversationService) recycleFileToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: recycleFileToolName,
		Desc: "Move a file to your Solar Drive recycle bin. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"file_id": {Type: schema.String, Desc: "The file ID to recycle", Required: true},
		}),
	}
}

func (s *ConversationService) restoreFileToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: restoreFileToolName,
		Desc: "Restore a file from your Solar Drive recycle bin. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"file_id": {Type: schema.String, Desc: "The file ID to restore", Required: true},
		}),
	}
}

func (s *ConversationService) listRecycleBinToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: listRecycleBinToolName,
		Desc: "List files in your Solar Drive recycle bin. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func (s *ConversationService) getStorageQuotaToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: getStorageQuotaToolName,
		Desc: "Get your Solar Drive storage quota and usage. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func (s *ConversationService) listWalletsToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: listWalletsToolName,
		Desc: "List your Solar wallets and balances. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func (s *ConversationService) listOrdersToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: listOrdersToolName,
		Desc: "List your Solar wallet orders. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func (s *ConversationService) listNotificationsToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: listNotificationsToolName,
		Desc: "List your Solar notifications. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func (s *ConversationService) getUnreadNotificationCountToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: getUnreadNotificationCountToolName,
		Desc: "Get the count of unread Solar notifications. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func (s *ConversationService) markAllNotificationsReadToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: markAllNotificationsReadToolName,
		Desc: "Mark all Solar notifications as read. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func (s *ConversationService) readWebpageToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: readWebpageToolName,
		Desc: "Read and extract metadata from a web page URL. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"url": {Type: schema.String, Desc: "The URL to read", Required: true},
		}),
	}
}

func (s *ConversationService) listStickersToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: listStickersToolName,
		Desc: "List available Solar sticker packs. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func (s *ConversationService) listSurveysToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: listSurveysToolName,
		Desc: "List your Solar surveys. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func (s *ConversationService) getMyLevelingToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: getMyLevelingToolName,
		Desc: "Get your Solar leveling and experience history. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func (s *ConversationService) listRelationshipsToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: listRelationshipsToolName,
		Desc: "List your Solar relationships (friends, blocked, muted, etc). Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}
}

func (s *ConversationService) followAccountToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: followAccountToolName,
		Desc: "Send a friend request to a Solar account. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"account_id": {Type: schema.String, Desc: "The account ID to follow", Required: true},
		}),
	}
}

func (s *ConversationService) unfollowAccountToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: unfollowAccountToolName,
		Desc: "Remove a relationship with a Solar account. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"account_id": {Type: schema.String, Desc: "The account ID to unfollow", Required: true},
		}),
	}
}

func (s *ConversationService) searchAccountsToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: searchAccountsToolName,
		Desc: "Search for Solar Network accounts by name or nickname. Acts on the user's own Solar account (connected via OAuth), not the bot's.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: schema.String, Desc: "Search query (matches account name or nickname)", Required: true},
		}),
	}
}

// ─── Dispatcher ────────────────────────────────────────────────────

func isUserScopedToolName(name string) bool {
	switch name {
	case listFilesToolName, getFileInfoToolName, createFolderToolName,
		uploadTextFileToolName, recycleFileToolName, restoreFileToolName,
		listRecycleBinToolName, getStorageQuotaToolName,
		listWalletsToolName, listOrdersToolName,
		listNotificationsToolName, getUnreadNotificationCountToolName, markAllNotificationsReadToolName,
		readWebpageToolName, listStickersToolName, listSurveysToolName,
		getMyLevelingToolName,
		listRelationshipsToolName, followAccountToolName, unfollowAccountToolName,
		searchAccountsToolName:
		return true
	}
	return false
}

func (s *ConversationService) executeUserScopedToolCall(ctx context.Context, agentID string, call schema.ToolCall) (*executedChatToolResult, error) {
	// 1. Resolve authenticated caller
	id, ok := CallerIdentityFrom(ctx)
	if !ok {
		return toolResultJSON(call, map[string]any{
			"ok":      false,
			"error":   "user_authorization_required",
			"message": "This tool acts on your Solar account. Connect your Solar account first via OAuth.",
		})
	}

	// 2. Get user access token via OAuth service
	if s.oauth == nil {
		return toolResultJSON(call, map[string]any{
			"ok":      false,
			"error":   "oauth_not_configured",
			"message": "OAuth is not configured. Contact the administrator.",
		})
	}
	token, err := s.oauth.UserAccessToken(ctx, agentID, id.AccountID)
	if err != nil {
		if err == ErrUserAuthRequired {
			return toolResultJSON(call, map[string]any{
				"ok":      false,
				"error":   "user_authorization_required",
				"message": "Your Solar account is not connected. Please connect it first via OAuth device flow.",
			})
		}
		return nil, fmt.Errorf("get user access token: %w", err)
	}

	// 3. Build client and dispatch
	client := solar_network.NewClientWithHTTP(s.cfg.SolarNetwork.BaseURL, token, s.netHTTP)

	switch call.Function.Name {
	case listFilesToolName:
		return s.executeListFiles(ctx, client, call)
	case getFileInfoToolName:
		return s.executeGetFileInfo(ctx, client, call)
	case createFolderToolName:
		return s.executeCreateFolder(ctx, client, call)
	case uploadTextFileToolName:
		return s.executeUploadTextFile(ctx, client, call)
	case recycleFileToolName:
		return s.executeRecycleFile(ctx, client, call)
	case restoreFileToolName:
		return s.executeRestoreFile(ctx, client, call)
	case listRecycleBinToolName:
		return s.executeListRecycleBin(ctx, client, call)
	case getStorageQuotaToolName:
		return s.executeGetStorageQuota(ctx, client, call)
	case listWalletsToolName:
		return s.executeListWallets(ctx, client, call)
	case listOrdersToolName:
		return s.executeListOrders(ctx, client, call)
	case listNotificationsToolName:
		return s.executeListNotifications(ctx, client, call)
	case getUnreadNotificationCountToolName:
		return s.executeGetUnreadNotificationCount(ctx, client, call)
	case markAllNotificationsReadToolName:
		return s.executeMarkAllNotificationsRead(ctx, client, call)
	case readWebpageToolName:
		return s.executeReadWebpage(ctx, client, call)
	case listStickersToolName:
		return s.executeListStickers(ctx, client, call)
	case listSurveysToolName:
		return s.executeListSurveys(ctx, client, call)
	case getMyLevelingToolName:
		return s.executeGetMyLeveling(ctx, client, call)
	case listRelationshipsToolName:
		return s.executeListRelationships(ctx, client, call)
	case followAccountToolName:
		return s.executeFollowAccount(ctx, client, call)
	case unfollowAccountToolName:
		return s.executeUnfollowAccount(ctx, client, call)
	case searchAccountsToolName:
		return s.executeSearchAccounts(ctx, client, call)
	default:
		return toolResultJSON(call, map[string]any{
			"ok":    false,
			"error": "unknown_tool",
		})
	}
}

// ─── Drive tools ───────────────────────────────────────────────────

func (s *ConversationService) executeListFiles(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	var input listFilesInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	q := url.Values{}
	if input.ParentID != "" {
		q.Set("parent_id", input.ParentID)
	}
	if input.IsFolder != nil {
		q.Set("is_folder", fmt.Sprintf("%t", *input.IsFolder))
	}
	if input.Name != "" {
		q.Set("name", input.Name)
	}
	if input.Take != nil {
		q.Set("take", fmt.Sprintf("%d", *input.Take))
	}
	if input.Offset != nil {
		q.Set("offset", fmt.Sprintf("%d", *input.Offset))
	}
	items, total, err := client.ListMyFiles(ctx, q)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "items": items, "total": total})
}

func (s *ConversationService) executeGetFileInfo(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	var input getFileInfoInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	item, err := client.GetFile(ctx, input.FileID)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "item": item})
}

func (s *ConversationService) executeCreateFolder(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	var input createFolderInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	parentID := ""
	if input.ParentID != nil {
		parentID = *input.ParentID
	}
	item, err := client.CreateFolder(ctx, input.Name, parentID)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "item": item})
}

func (s *ConversationService) executeUploadTextFile(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	var input uploadTextFileInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	parentID := ""
	if input.ParentID != nil {
		parentID = *input.ParentID
	}
	item, err := client.UploadTextFile(ctx, input.Name, parentID, []byte(input.Content))
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "item": item})
}

func (s *ConversationService) executeRecycleFile(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	var input recycleFileInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	if err := client.RecycleFile(ctx, input.FileID); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true})
}

func (s *ConversationService) executeRestoreFile(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	var input restoreFileInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	if err := client.RestoreFile(ctx, input.FileID); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true})
}

func (s *ConversationService) executeListRecycleBin(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	var input listRecycleBinInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	take, offset := defaultPagination(input.Take, input.Offset)
	items, total, err := client.ListRecycleBin(ctx, take, offset)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "items": items, "total": total})
}

func (s *ConversationService) executeGetStorageQuota(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	quota, err := client.GetStorageQuota(ctx)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, quota)
}

// ─── Wallet tools ──────────────────────────────────────────────────

func (s *ConversationService) executeListWallets(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	items, err := client.ListWallets(ctx)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "items": items})
}

func (s *ConversationService) executeListOrders(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	var input listOrdersInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	take, offset := defaultPagination(input.Take, input.Offset)
	items, total, err := client.ListOrders(ctx, take, offset)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "items": items, "total": total})
}

// ─── Notification tools ────────────────────────────────────────────

func (s *ConversationService) executeListNotifications(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	var input listNotificationsInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	take, offset := defaultPagination(input.Take, input.Offset)
	items, total, err := client.ListNotifications(ctx, take, offset)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "items": items, "total": total})
}

func (s *ConversationService) executeGetUnreadNotificationCount(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	count, err := client.UnreadNotificationCount(ctx)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "count": count})
}

func (s *ConversationService) executeMarkAllNotificationsRead(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	if err := client.MarkAllNotificationsRead(ctx); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true})
}

// ─── Sphere tools ──────────────────────────────────────────────────

func (s *ConversationService) executeReadWebpage(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	var input readWebpageInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	item, err := client.ReadWebpage(ctx, input.URL)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "item": item})
}

func (s *ConversationService) executeListStickers(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	var input listStickersInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	take, offset := defaultPagination(input.Take, input.Offset)
	items, total, err := client.ListStickers(ctx, take, offset)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "items": items, "total": total})
}

func (s *ConversationService) executeListSurveys(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	var input listSurveysInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	take, offset := defaultPagination(input.Take, input.Offset)
	items, total, err := client.ListSurveys(ctx, take, offset)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "items": items, "total": total})
}

// ─── Passport tools ────────────────────────────────────────────────

func (s *ConversationService) executeGetMyLeveling(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	items, total, err := client.GetMyLeveling(ctx)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "items": items, "total": total})
}

// ─── Stargate tools ────────────────────────────────────────────────

func (s *ConversationService) executeListRelationships(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	items, err := client.ListRelationships(ctx)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "items": items})
}

func (s *ConversationService) executeFollowAccount(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	var input followAccountInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	if err := client.FollowAccount(ctx, input.AccountID); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true})
}

func (s *ConversationService) executeUnfollowAccount(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	var input unfollowAccountInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	if err := client.UnfollowAccount(ctx, input.AccountID); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true})
}

func (s *ConversationService) executeSearchAccounts(ctx context.Context, client *solar_network.Client, call schema.ToolCall) (*executedChatToolResult, error) {
	var input searchAccountsInput
	if err := decodeToolCallArgs(call, &input); err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	take, offset := defaultPagination(input.Take, input.Offset)
	items, total, err := client.SearchAccounts(ctx, input.Query, take, offset)
	if err != nil {
		return toolResultJSON(call, map[string]any{"ok": false, "error": err.Error()})
	}
	return toolResultJSON(call, map[string]any{"ok": true, "items": items, "total": total})
}

// ─── Helpers ───────────────────────────────────────────────────────

func defaultPagination(take, offset *int) (int, int) {
	t := 20
	o := 0
	if take != nil && *take > 0 {
		t = *take
	}
	if offset != nil && *offset >= 0 {
		o = *offset
	}
	return t, o
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// NewID generates a ULID for use as a primary key.
func NewID() string {
	return ulid.Make().String()
}

// ─── ConversationService OAuth passthrough methods

// ─── ConversationService OAuth passthrough methods ─────────────────

func (s *ConversationService) StartOAuthDeviceFlow(ctx context.Context, agentID, accountID string) (*DeviceFlowInfo, error) {
	if s.oauth == nil {
		return nil, fmt.Errorf("oauth is not configured")
	}
	return s.oauth.StartDeviceFlow(ctx, agentID, accountID)
}

func (s *ConversationService) OAuthStatus(ctx context.Context, agentID, accountID string) (status, scopes string, expiresAt *time.Time, err error) {
	if s.oauth == nil {
		return "none", "", nil, nil
	}
	return s.oauth.Status(ctx, agentID, accountID)
}

func (s *ConversationService) RevokeOAuth(ctx context.Context, agentID, accountID string) error {
	if s.oauth == nil {
		return fmt.Errorf("oauth is not configured")
	}
	return s.oauth.Revoke(ctx, agentID, accountID)
}
