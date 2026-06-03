// Package telegram owns the concrete Telegram Bot API client and the
// durable long-polling runtime.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	botapi "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/invite"
	"github.com/justskiv/gatekeeper/internal/messages"
)

const defaultServerURL = "https://api.telegram.org"

// DefaultAllowedUpdates is the explicit update surface requested from
// Telegram on every poll.
var DefaultAllowedUpdates = []string{
	models.AllowedUpdateMessage,
	models.AllowedUpdateCallbackQuery,
	models.AllowedUpdateMyChatMember,
	models.AllowedUpdateChatMember,
	models.AllowedUpdateChatJoinRequest,
}

// Client is the one concrete Telegram client used by the application.
type Client struct {
	bot            *botapi.Bot
	token          string
	serverURL      string
	httpClient     *http.Client
	allowedUpdates []string
}

// ClientOption configures Client construction.
type ClientOption func(*clientOptions)

type clientOptions struct {
	serverURL      string
	httpClient     *http.Client
	allowedUpdates []string
}

// WithServerURL points the client to a Bot API-compatible endpoint.
func WithServerURL(url string) ClientOption {
	return func(o *clientOptions) {
		o.serverURL = strings.TrimRight(url, "/")
	}
}

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(o *clientOptions) {
		o.httpClient = client
	}
}

// WithAllowedUpdates replaces the default explicit allowed_updates list.
func WithAllowedUpdates(updates []string) ClientOption {
	return func(o *clientOptions) {
		o.allowedUpdates = append([]string(nil), updates...)
	}
}

// NewClient creates a Telegram client without validating the token. The
// runtime calls GetMe explicitly so startup order stays visible.
func NewClient(token string, opts ...ClientOption) (*Client, error) {
	options := clientOptions{
		serverURL:      defaultServerURL,
		httpClient:     &http.Client{Timeout: time.Minute},
		allowedUpdates: append([]string(nil), DefaultAllowedUpdates...),
	}
	for _, opt := range opts {
		opt(&options)
	}

	if strings.TrimSpace(token) == "" {
		return nil, errors.New("empty telegram token")
	}

	b, err := botapi.New(token,
		botapi.WithSkipGetMe(),
		botapi.WithServerURL(options.serverURL),
		botapi.WithHTTPClient(time.Minute, options.httpClient),
		botapi.WithAllowedUpdates(botapi.AllowedUpdates(options.allowedUpdates)))
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}

	return &Client{
		bot:            b,
		token:          token,
		serverURL:      options.serverURL,
		httpClient:     options.httpClient,
		allowedUpdates: options.allowedUpdates,
	}, nil
}

// BotID returns the numeric ID encoded in the token prefix.
func (c *Client) BotID() int64 {
	return c.bot.ID()
}

// GetMe verifies the token and returns the bot user.
func (c *Client) GetMe(ctx context.Context) (*models.User, error) {
	user, err := c.bot.GetMe(ctx)
	if err != nil {
		return nil, NormalizeError("getMe", err)
	}

	return user, nil
}

// GetChat returns full chat information.
func (c *Client) GetChat(ctx context.Context, chatID int64) (*models.ChatFullInfo, error) {
	chat, err := c.bot.GetChat(ctx, &botapi.GetChatParams{ChatID: chatID})
	if err != nil {
		return nil, NormalizeError("getChat", err)
	}

	return chat, nil
}

// GetChatMember returns one member status in a chat.
func (c *Client) GetChatMember(
	ctx context.Context, chatID, userID int64,
) (*models.ChatMember, error) {
	member, err := c.bot.GetChatMember(ctx, &botapi.GetChatMemberParams{
		ChatID: chatID,
		UserID: userID,
	})
	if err != nil {
		return nil, NormalizeError("getChatMember", err)
	}

	return member, nil
}

// SendMessage sends a plain text message.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	return c.sendMessage(ctx, chatID, text, "", nil)
}

// SendFormattedMessage sends a renderer-produced formatted message.
func (c *Client) SendFormattedMessage(
	ctx context.Context,
	chatID int64,
	text string,
	parseMode string,
) error {
	return c.sendMessage(ctx, chatID, text, parseMode, nil)
}

// SendMessageWithReplyMarkup sends a plain text message with reply markup.
func (c *Client) SendMessageWithReplyMarkup(
	ctx context.Context,
	chatID int64,
	text string,
	replyMarkup models.ReplyMarkup,
) error {
	return c.sendMessage(ctx, chatID, text, "", replyMarkup)
}

// SendFormattedMessageWithReplyMarkup sends formatted text with reply markup.
func (c *Client) SendFormattedMessageWithReplyMarkup(
	ctx context.Context,
	chatID int64,
	text string,
	parseMode string,
	replyMarkup models.ReplyMarkup,
) error {
	return c.sendMessage(ctx, chatID, text, parseMode, replyMarkup)
}

func (c *Client) sendMessage(
	ctx context.Context,
	chatID int64,
	text string,
	parseMode string,
	replyMarkup models.ReplyMarkup,
) error {
	params := &botapi.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	}
	if parseMode != "" {
		params.ParseMode = models.ParseMode(parseMode)
	}
	if replyMarkup != nil {
		params.ReplyMarkup = replyMarkup
	}

	_, err := c.bot.SendMessage(ctx, params)
	if err != nil {
		if chatID > 0 {
			return NormalizeError("sendMessage", err)
		}

		return NormalizeError("sendChatMessage", err)
	}

	return nil
}

// CreateChatInviteLink creates a managed invite link.
func (c *Client) CreateChatInviteLink(
	ctx context.Context,
	params CreateChatInviteLinkParams,
) (*models.ChatInviteLink, error) {
	var expireDate int
	if params.ExpireAt != nil {
		expireDate = int(params.ExpireAt.Unix())
	}

	wire := createChatInviteLinkRequest{
		ChatID:             params.ChatID,
		Name:               params.Name,
		ExpireDate:         expireDate,
		MemberLimit:        params.MemberLimit,
		CreatesJoinRequest: params.CreatesJoinRequest,
	}

	var link models.ChatInviteLink
	if err := c.rawRequest(ctx, "createChatInviteLink", wire, &link); err != nil {
		return nil, NormalizeError("createChatInviteLink", err)
	}

	return &link, nil
}

// RevokeChatInviteLink revokes one managed invite link.
func (c *Client) RevokeChatInviteLink(
	ctx context.Context,
	chatID int64,
	inviteLink string,
) (*models.ChatInviteLink, error) {
	var link models.ChatInviteLink
	if err := c.rawRequest(ctx, "revokeChatInviteLink", map[string]any{
		"chat_id":     chatID,
		"invite_link": inviteLink,
	}, &link); err != nil {
		return nil, NormalizeError("revokeChatInviteLink", err)
	}

	return &link, nil
}

// ApproveChatJoinRequest approves one pending join request.
func (c *Client) ApproveChatJoinRequest(
	ctx context.Context,
	chatID, userID int64,
) error {
	if err := c.rawRequest(ctx, "approveChatJoinRequest", map[string]any{
		"chat_id": chatID,
		"user_id": userID,
	}, nil); err != nil {
		return NormalizeError("approveChatJoinRequest", err)
	}

	return nil
}

// DeclineChatJoinRequest declines one pending join request.
func (c *Client) DeclineChatJoinRequest(
	ctx context.Context,
	chatID, userID int64,
) error {
	if err := c.rawRequest(ctx, "declineChatJoinRequest", map[string]any{
		"chat_id": chatID,
		"user_id": userID,
	}, nil); err != nil {
		return NormalizeError("declineChatJoinRequest", err)
	}

	return nil
}

// BanChatMember bans one user from a chat or channel.
func (c *Client) BanChatMember(ctx context.Context, chatID, userID int64) error {
	if err := c.rawRequest(ctx, "banChatMember", map[string]any{
		"chat_id": chatID,
		"user_id": userID,
	}, nil); err != nil {
		return NormalizeError("banChatMember", err)
	}

	return nil
}

// UnbanChatMember unbans one user from a chat or channel.
func (c *Client) UnbanChatMember(
	ctx context.Context,
	chatID, userID int64,
	onlyIfBanned bool,
) error {
	if err := c.rawRequest(ctx, "unbanChatMember", map[string]any{
		"chat_id":        chatID,
		"user_id":        userID,
		"only_if_banned": onlyIfBanned,
	}, nil); err != nil {
		return NormalizeError("unbanChatMember", err)
	}

	return nil
}

// SetMyCommands registers user commands globally and owner commands in
// each owner private chat scope.
func (c *Client) SetMyCommands(ctx context.Context, ownerIDs []int64) error {
	userCommands := []models.BotCommand{
		{Command: "start", Description: messages.CommandStartDescription},
		{Command: "help", Description: messages.CommandHelpDescription},
		{Command: "status", Description: messages.CommandStatusDescription},
	}
	if _, err := c.bot.SetMyCommands(ctx, &botapi.SetMyCommandsParams{
		Commands: userCommands,
		Scope:    &models.BotCommandScopeDefault{},
	}); err != nil {
		return NormalizeError("setMyCommands", err)
	}

	ownerCommands := append([]models.BotCommand(nil), userCommands...)

	ownerCommands = append(ownerCommands,
		models.BotCommand{
			Command:     "here",
			Description: messages.CommandHereDescription,
		},
		models.BotCommand{
			Command:     "whois",
			Description: messages.CommandWhoisDescription,
		},
		models.BotCommand{
			Command:     "grant",
			Description: messages.CommandGrantDescription,
		},
		models.BotCommand{
			Command:     "revoke",
			Description: messages.CommandRevokeDescription,
		},
		models.BotCommand{
			Command:     "ban",
			Description: messages.CommandBanDescription,
		},
		models.BotCommand{
			Command:     "unban",
			Description: messages.CommandUnbanDescription,
		},
		models.BotCommand{
			Command:     "sync",
			Description: messages.CommandSyncDescription,
		},
		models.BotCommand{
			Command:     "stats",
			Description: messages.CommandStatsDescription,
		},
		models.BotCommand{
			Command:     "alerts",
			Description: messages.CommandAlertsDescription,
		},
		models.BotCommand{
			Command:     "export",
			Description: messages.CommandExportDescription,
		},
		models.BotCommand{
			Command:     "chats",
			Description: messages.CommandChatsDescription,
		},
		models.BotCommand{
			Command:     "help_admin",
			Description: messages.CommandHelpAdminDescription,
		})
	for _, ownerID := range ownerIDs {
		if _, err := c.bot.SetMyCommands(ctx, &botapi.SetMyCommandsParams{
			Commands: ownerCommands,
			Scope:    &models.BotCommandScopeChat{ChatID: ownerID},
		}); err != nil {
			return NormalizeError("setMyCommands", err)
		}
	}

	return nil
}

// SetWebhook registers Telegram webhook delivery with an explicit update set.
func (c *Client) SetWebhook(
	ctx context.Context,
	url string,
	secretToken string,
	allowedUpdates []string,
) error {
	if len(allowedUpdates) == 0 {
		allowedUpdates = append([]string(nil), c.allowedUpdates...)
	}

	if err := c.rawRequest(ctx, "setWebhook", map[string]any{
		"url":             url,
		"secret_token":    secretToken,
		"allowed_updates": allowedUpdates,
	}, nil); err != nil {
		return NormalizeError("setWebhook", err)
	}

	return nil
}

// DeleteWebhook disables Telegram webhook delivery so getUpdates can run.
func (c *Client) DeleteWebhook(ctx context.Context) error {
	if err := c.rawRequest(ctx, "deleteWebhook", map[string]any{
		"drop_pending_updates": false,
	}, nil); err != nil {
		return NormalizeError("deleteWebhook", err)
	}

	return nil
}

// GetUpdates calls Telegram getUpdates directly. The upstream package's
// polling method is intentionally internal because it feeds its handler
// channel; this runtime must persist updates before dispatching them.
func (c *Client) GetUpdates(
	ctx context.Context, params GetUpdatesParams,
) ([]FetchedUpdate, error) {
	if params.Timeout == 0 {
		params.Timeout = 50
	}

	if len(params.AllowedUpdates) == 0 {
		params.AllowedUpdates = append([]string(nil), c.allowedUpdates...)
	}

	var rawUpdates []json.RawMessage
	if err := c.rawRequest(ctx, "getUpdates", params, &rawUpdates); err != nil {
		return nil, NormalizeError("getUpdates", err)
	}

	updates := make([]FetchedUpdate, 0, len(rawUpdates))
	for _, raw := range rawUpdates {
		var update models.Update
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, fmt.Errorf("decode telegram update: %w", err)
		}

		updates = append(updates, FetchedUpdate{
			Raw:    append(json.RawMessage(nil), raw...),
			Update: &update,
		})
	}

	return updates, nil
}

// FetchedUpdate is one raw getUpdates result plus its decoded model.
type FetchedUpdate struct {
	Raw    json.RawMessage
	Update *models.Update
}

// GetUpdatesParams is the subset of getUpdates fields used by the poller.
type GetUpdatesParams struct {
	Offset         int64    `json:"offset,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	Timeout        int      `json:"timeout,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}

// CreateChatInviteLinkParams keeps the Telegram client surface stable while
// the invite package owns the consumer-side request shape.
type CreateChatInviteLinkParams = invite.CreateChatInviteLinkParams

type createChatInviteLinkRequest struct {
	ChatID             int64  `json:"chat_id"`
	Name               string `json:"name,omitempty"`
	ExpireDate         int    `json:"expire_date,omitempty"`
	MemberLimit        int    `json:"member_limit,omitempty"`
	CreatesJoinRequest bool   `json:"creates_join_request"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result,omitempty"`
	Description string          `json:"description,omitempty"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Parameters  struct {
		RetryAfter      int `json:"retry_after,omitempty"`
		MigrateToChatID int `json:"migrate_to_chat_id,omitempty"`
	} `json:"parameters"`
}

func (c *Client) rawRequest(
	ctx context.Context, method string, params any, dest any,
) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode telegram %s request: %w", method, err)
	}

	url := c.serverURL + "/bot" + c.token + "/" + method

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create telegram %s request: %w", method, err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do telegram %s request: %s",
			method, strings.ReplaceAll(err.Error(), c.token, "***"))
	}
	defer func() { _ = resp.Body.Close() }()

	var apiResp apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return fmt.Errorf("decode telegram %s response: %w", method, err)
	}

	if !apiResp.OK {
		return telegramAPIError(method, apiResp)
	}

	if dest != nil {
		if err := json.Unmarshal(apiResp.Result, dest); err != nil {
			return fmt.Errorf("decode telegram %s result: %w", method, err)
		}
	}

	return nil
}

func telegramAPIError(method string, resp apiResponse) error {
	switch resp.ErrorCode {
	case http.StatusForbidden:
		return fmt.Errorf("%w, %s", botapi.ErrorForbidden, resp.Description)
	case http.StatusBadRequest:
		return fmt.Errorf("%w, %s", botapi.ErrorBadRequest, resp.Description)
	case http.StatusUnauthorized:
		return fmt.Errorf("%w, %s", botapi.ErrorUnauthorized, resp.Description)
	case http.StatusNotFound:
		return fmt.Errorf("%w, %s", botapi.ErrorNotFound, resp.Description)
	case http.StatusConflict:
		return fmt.Errorf("%w, %s", botapi.ErrorConflict, resp.Description)
	case http.StatusTooManyRequests:
		return &botapi.TooManyRequestsError{
			Message:    fmt.Sprintf("%s, %s", botapi.ErrorTooManyRequests, resp.Description),
			RetryAfter: resp.Parameters.RetryAfter,
		}
	default:
		return fmt.Errorf("telegram %s failed with %d: %s",
			method, resp.ErrorCode, resp.Description)
	}
}

// ErrorCategory is a caller-facing Telegram error class.
type ErrorCategory string

const (
	ErrorCategoryRateLimited     ErrorCategory = "rate_limited"
	ErrorCategoryDMBlocked       ErrorCategory = "dm_blocked"
	ErrorCategoryPermanentRights ErrorCategory = "permanent_rights"
	ErrorCategoryForbidden       ErrorCategory = "forbidden"
	ErrorCategoryUnauthorized    ErrorCategory = "unauthorized"
	ErrorCategoryConflict        ErrorCategory = "conflict"
	ErrorCategoryBadRequest      ErrorCategory = "bad_request"
	ErrorCategoryNotFound        ErrorCategory = "not_found"
	ErrorCategoryOther           ErrorCategory = "other"
)

// APIError wraps Bot API errors with a stable category.
type APIError struct {
	Method     string
	Category   ErrorCategory
	RetryAfter int
	Err        error
}

func (e *APIError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%s: %s: retry_after=%d",
			e.Method, e.Category, e.RetryAfter)
	}

	return fmt.Sprintf("%s: %s: %v", e.Method, e.Category, e.Err)
}

func (e *APIError) Unwrap() error {
	return e.Err
}

// TelegramCategory exposes a stable string category to packages that
// should not import telegram just to classify best-effort send errors.
func (e *APIError) TelegramCategory() string {
	return string(e.Category)
}

// NormalizeError maps Bot API errors to typed categories.
func NormalizeError(method string, err error) error {
	if err == nil {
		return nil
	}

	var rateLimit *botapi.TooManyRequestsError
	if errors.As(err, &rateLimit) {
		return &APIError{
			Method:     method,
			Category:   ErrorCategoryRateLimited,
			RetryAfter: rateLimit.RetryAfter,
			Err:        err,
		}
	}

	category := ErrorCategoryOther

	switch {
	case method == "sendMessage" && errors.Is(err, botapi.ErrorForbidden):
		category = ErrorCategoryDMBlocked
	case isPermanentRightsText(err):
		category = ErrorCategoryPermanentRights
	case errors.Is(err, botapi.ErrorForbidden):
		category = ErrorCategoryForbidden
	case errors.Is(err, botapi.ErrorUnauthorized):
		category = ErrorCategoryUnauthorized
	case errors.Is(err, botapi.ErrorConflict):
		category = ErrorCategoryConflict
	case errors.Is(err, botapi.ErrorBadRequest):
		category = ErrorCategoryBadRequest
	case errors.Is(err, botapi.ErrorNotFound):
		category = ErrorCategoryNotFound
	}

	return &APIError{Method: method, Category: category, Err: err}
}

// IsRateLimited reports whether err is a normalized 429.
func IsRateLimited(err error) bool {
	var apiErr *APIError

	return errors.As(err, &apiErr) &&
		apiErr.Category == ErrorCategoryRateLimited
}

// IsDMBlocked reports whether err is a normalized blocked-DM response.
func IsDMBlocked(err error) bool {
	var apiErr *APIError

	return errors.As(err, &apiErr) &&
		apiErr.Category == ErrorCategoryDMBlocked
}

// IsPermanentRights reports whether err is a non-retryable rights issue.
func IsPermanentRights(err error) bool {
	var apiErr *APIError

	return errors.As(err, &apiErr) &&
		apiErr.Category == ErrorCategoryPermanentRights
}

// IsForbidden reports whether err is a normalized Telegram 403.
func IsForbidden(err error) bool {
	var apiErr *APIError

	return errors.As(err, &apiErr) &&
		apiErr.Category == ErrorCategoryForbidden
}

// RetryAfter returns Telegram retry_after when err is a normalized 429.
func RetryAfter(err error) (time.Duration, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) ||
		apiErr.Category != ErrorCategoryRateLimited ||
		apiErr.RetryAfter <= 0 {
		return 0, false
	}

	return time.Duration(apiErr.RetryAfter) * time.Second, true
}

func isPermanentRightsText(err error) bool {
	msg := strings.ToLower(err.Error())

	patterns := []string{
		"bot is not a member",
		"not enough rights",
		"not an administrator",
		"need administrator rights",
		"have no rights",
	}
	for _, pattern := range patterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}

	return false
}

// IsExpectedNoop reports whether a Bot API error means the action is
// already satisfied or no longer meaningful.
func IsExpectedNoop(action string, err error) bool {
	action = strings.ToLower(action)
	switch action {
	case "ban_chat_member", "banchatmember", "soft_kick",
		"unban_chat_member", "unbanchatmember", "unban",
		"approve_chat_join_request", "approvechatjoinrequest", "approve_join",
		"decline_chat_join_request", "declinechatjoinrequest", "decline_join",
		"revoke_chat_invite_link", "revokechatinvitelink", "revoke_invite":
	default:
		return false
	}

	msg := strings.ToLower(err.Error())

	patterns := []string{
		"user_not_participant",
		"user not found",
		"user is not a member",
		"user is already",
		"already a member",
		"already banned",
		"already unbanned",
		"invite link is already revoked",
		"request not found",
		"invite request not found",
		"invite link not found",
	}
	for _, pattern := range patterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}

	return false
}
