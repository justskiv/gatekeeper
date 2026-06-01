package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/admission"
	commandbot "github.com/justskiv/gatekeeper/internal/bot"
	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/notify"
	"github.com/justskiv/gatekeeper/internal/source"
	"github.com/justskiv/gatekeeper/internal/store"
)

// OutboundKind tells the poller how to deliver a post-commit effect.
type OutboundKind string

const (
	OutboundChatMessage OutboundKind = "chat_message"
	OutboundDM          OutboundKind = "dm"
)

// OutboundMessage is a Telegram call to make after tx2 commits.
type OutboundMessage struct {
	Kind   OutboundKind
	ChatID int64
	TGID   int64
	Text   string
}

// RouteResult describes how an update reached a terminal state.
type RouteResult struct {
	Status  store.TelegramUpdateStatus
	Effects []OutboundMessage
}

// Router dispatches persisted updates to phase-specific handlers.
type Router struct {
	users         *store.Users
	subscriptions *store.Subscriptions
	grants        *store.Grants
	meta          *store.Meta
	audit         *store.Audit
	alerts        *store.Alerts
	whitelist     *store.Whitelist
	revocations   *store.Revocations
	outbox        *store.Outbox
	invites       *store.Invites
	updateID      int64
	statusEngine  *engine.Engine
	sourceChats   SourceChats
	admissionCfg  admission.Config
	preflight     RoutePreflight
	chats         []HealthChat
	ownerIDs      []int64
	logger        *slog.Logger
}

// RouterDeps are transaction-bound repositories for one update.
type RouterDeps struct {
	Users         *store.Users
	Subscriptions *store.Subscriptions
	Grants        *store.Grants
	Meta          *store.Meta
	Audit         *store.Audit
	Alerts        *store.Alerts
	Whitelist     *store.Whitelist
	Revocations   *store.Revocations
	Outbox        *store.Outbox
	Invites       *store.Invites
	UpdateID      int64
}

// SourceChats identifies configured subscription source chats.
type SourceChats struct {
	BoostyGroupID      int64
	TributeChannelID   int64
	TributeObservation bool
}

// RoutePreflight contains network reads performed before tx2.
type RoutePreflight struct {
	Snapshot             *engine.Snapshot
	AdmissionRateLimited bool
}

// RouterOption configures optional routes.
type RouterOption func(*Router)

// WithStatusEngine attaches the status engine to the router.
func WithStatusEngine(e *engine.Engine) RouterOption {
	return func(r *Router) {
		r.statusEngine = e
	}
}

// WithSourceChats attaches source-chat routing config.
func WithSourceChats(chats SourceChats) RouterOption {
	return func(r *Router) {
		r.sourceChats = chats
	}
}

// WithAdmissionConfig attaches managed club-resource admission config.
func WithAdmissionConfig(cfg admission.Config) RouterOption {
	return func(r *Router) {
		r.admissionCfg = cfg
	}
}

// WithPreflight attaches pre-transaction live observations.
func WithPreflight(preflight RoutePreflight) RouterOption {
	return func(r *Router) {
		r.preflight = preflight
	}
}

// NewRouter returns a router bound to one tx2 repository set.
func NewRouter(
	deps RouterDeps,
	chats []HealthChat,
	ownerIDs []int64,
	logger *slog.Logger,
	opts ...RouterOption,
) *Router {
	if logger == nil {
		logger = slog.Default()
	}

	r := &Router{
		users:         deps.Users,
		subscriptions: deps.Subscriptions,
		grants:        deps.Grants,
		meta:          deps.Meta,
		audit:         deps.Audit,
		alerts:        deps.Alerts,
		whitelist:     deps.Whitelist,
		revocations:   deps.Revocations,
		outbox:        deps.Outbox,
		invites:       deps.Invites,
		updateID:      deps.UpdateID,
		chats:         chats,
		ownerIDs:      ownerIDs,
		logger:        logger,
	}
	for _, opt := range opts {
		opt(r)
	}

	return r
}

// Route applies domain changes for an update without calling Telegram.
func (r *Router) Route(ctx context.Context, update *models.Update) (RouteResult, error) {
	switch {
	case update == nil:
		return ignored(), nil
	case update.Message != nil:
		return r.routeMessage(ctx, update.Message)
	case update.MyChatMember != nil:
		effects, err := handleMyChatMember(ctx, healthRepos{
			users:  r.users,
			meta:   r.meta,
			audit:  r.audit,
			alerts: r.alerts,
		}, r.chats, r.ownerIDs, update.MyChatMember, r.logger)
		if err != nil {
			return RouteResult{}, err
		}

		return r.processedEffects(ctx, effects)
	case update.ChatMember != nil:
		return r.routeChatMember(ctx, update.ChatMember)
	case update.ChatJoinRequest != nil:
		return r.routeChatJoinRequest(ctx, update.ChatJoinRequest)
	case update.CallbackQuery != nil:
		return r.routeCallback(ctx, update.CallbackQuery)
	default:
		return ignored(), nil
	}
}

func (r *Router) routeMessage(
	ctx context.Context, msg *models.Message,
) (RouteResult, error) {
	if msg.Chat.Type == models.ChatTypePrivate &&
		r.canRunAdmission() &&
		isAccessRequestMessage(msg) {
		return r.routeAdmissionAccess(ctx, userFromTelegram(*msg.From, domain.DMOpen),
			"message")
	}

	commands := commandbot.NewCommands(commandbot.CommandDeps{
		Users:         r.users,
		Subscriptions: r.subscriptions,
		Grants:        r.grants,
		Audit:         r.audit,
		Whitelist:     r.whitelist,
		StatusEngine:  r.statusEngine,
		Preflight:     r.preflight.Snapshot,
	}, r.ownerIDs)
	if msg.Chat.Type == models.ChatTypePrivate {
		result, err := commands.HandlePrivate(ctx, msg)
		if err != nil {
			return RouteResult{}, err
		}

		return r.fromCommandResult(ctx, result)
	}

	return r.fromCommandResult(ctx, commands.HandleHere(msg))
}

func (r *Router) routeChatMember(
	ctx context.Context,
	update *models.ChatMemberUpdated,
) (RouteResult, error) {
	if resource, ok := r.clubResource(update.Chat.ID); ok && r.canRunAdmission() {
		return r.routeClubMembership(ctx, update, resource)
	}

	platform, ok := r.sourcePlatform(update.Chat.ID)
	if !ok {
		return ignored(), nil
	}

	if r.statusEngine == nil {
		return processed(nil), nil
	}

	event, ok := normalizeSubscriptionEvent(update, platform)
	if !ok {
		return processed(nil), nil
	}

	effects, err := r.statusEngine.HandleEvent(ctx, engine.Store{
		Users:         r.users,
		Subscriptions: r.subscriptions,
		Audit:         r.audit,
		Revocations:   r.revocations,
		Whitelist:     r.whitelist,
	}, event)
	if err != nil {
		return RouteResult{}, err
	}

	return r.processedEffects(ctx, fromEngineEffects(effects))
}

func (r *Router) routeChatJoinRequest(
	ctx context.Context,
	req *models.ChatJoinRequest,
) (RouteResult, error) {
	resource, ok := r.clubResource(req.Chat.ID)
	if !ok || !r.canRunAdmission() {
		return ignored(), nil
	}

	inviteLink := ""
	if req.InviteLink != nil {
		inviteLink = req.InviteLink.InviteLink
	}

	requestDate := time.Now()
	if req.Date > 0 {
		requestDate = time.Unix(int64(req.Date), 0)
	}

	handler := r.admissionHandler()
	if err := handler.HandleJoinRequest(ctx, admission.JoinRequest{
		User:        userFromTelegram(req.From, ""),
		UserChatID:  req.UserChatID,
		Resource:    resource,
		InviteLink:  inviteLink,
		RequestDate: requestDate,
		Snapshot:    r.preflight.Snapshot,
	}); err != nil {
		return RouteResult{}, err
	}

	return processed(nil), nil
}

func (r *Router) routeCallback(
	ctx context.Context,
	query *models.CallbackQuery,
) (RouteResult, error) {
	if query == nil || query.Data != messages.RetryAccessCallbackData ||
		!r.canRunAdmission() {
		return ignored(), nil
	}

	return r.routeAdmissionAccess(ctx,
		userFromTelegram(query.From, domain.DMOpen), "callback")
}

func (r *Router) routeAdmissionAccess(
	ctx context.Context,
	user domain.User,
	trigger string,
) (RouteResult, error) {
	handler := r.admissionHandler()
	if err := handler.HandleAccessRequest(ctx, admission.AccessRequest{
		User:        user,
		Snapshot:    r.preflight.Snapshot,
		RateLimited: r.preflight.AdmissionRateLimited,
		Trigger:     trigger,
	}); err != nil {
		return RouteResult{}, err
	}

	return processed(nil), nil
}

func (r *Router) routeClubMembership(
	ctx context.Context,
	update *models.ChatMemberUpdated,
	resource domain.Resource,
) (RouteResult, error) {
	user := chatMemberUser(&update.NewChatMember)
	if user == nil || user.IsBot {
		return processed(nil), nil
	}

	oldInChat := source.MemberInChat(&update.OldChatMember)

	newInChat := source.MemberInChat(&update.NewChatMember)
	if oldInChat == newInChat {
		return processed(nil), nil
	}

	inviteLink := ""
	if update.InviteLink != nil {
		inviteLink = update.InviteLink.InviteLink
	}

	eventDate := time.Time{}
	if update.Date > 0 {
		eventDate = time.Unix(int64(update.Date), 0)
	}

	handler := r.admissionHandler()
	if err := handler.HandleMembershipUpdate(ctx, admission.MembershipUpdate{
		User:           userFromTelegram(*user, ""),
		Resource:       resource,
		Joined:         newInChat,
		ViaJoinRequest: update.ViaJoinRequest,
		InviteLink:     inviteLink,
		EventDate:      eventDate,
		Snapshot:       r.preflight.Snapshot,
	}); err != nil {
		return RouteResult{}, err
	}

	return processed(nil), nil
}

func (r *Router) admissionHandler() *admission.Handler {
	return admission.New(admission.Deps{
		Users:         r.users,
		Subscriptions: r.subscriptions,
		Grants:        r.grants,
		Invites:       r.invites,
		Outbox:        r.outbox,
		Audit:         r.audit,
		Alerts:        r.alerts,
		Whitelist:     r.whitelist,
		Revocations:   r.revocations,
		StatusEngine:  r.statusEngine,
	}, r.admissionCfg)
}

func (r *Router) canRunAdmission() bool {
	return r.statusEngine != nil &&
		r.users != nil &&
		r.subscriptions != nil &&
		r.grants != nil &&
		r.invites != nil &&
		r.outbox != nil
}

func isAccessRequestMessage(msg *models.Message) bool {
	if msg == nil || msg.From == nil {
		return false
	}

	cmd := commandbot.CommandName(msg.Text)

	return cmd == "start" || cmd == ""
}

func (r *Router) clubResource(chatID int64) (domain.Resource, bool) {
	if chatID == 0 {
		return "", false
	}

	for _, resource := range r.admissionCfg.Resources {
		if resource.ChatID == chatID {
			return resource.Resource, true
		}
	}

	switch chatID {
	case r.admissionCfg.ClubChatID:
		return domain.ResourceChat, r.admissionCfg.ClubChatID != 0
	case r.admissionCfg.ClubChannelID:
		return domain.ResourceChannel, r.admissionCfg.ClubChannelID != 0
	default:
		return "", false
	}
}

func (r *Router) sourcePlatform(chatID int64) (domain.Platform, bool) {
	switch {
	case chatID != 0 && chatID == r.sourceChats.BoostyGroupID:
		return domain.PlatformBoosty, true
	case r.sourceChats.TributeObservation &&
		chatID != 0 &&
		chatID == r.sourceChats.TributeChannelID:
		return domain.PlatformTribute, true
	default:
		return "", false
	}
}

func normalizeSubscriptionEvent(
	update *models.ChatMemberUpdated,
	platform domain.Platform,
) (domain.SubscriptionEvent, bool) {
	user := chatMemberUser(&update.NewChatMember)
	if user == nil || user.IsBot {
		return domain.SubscriptionEvent{}, false
	}

	oldInChat := source.MemberInChat(&update.OldChatMember)

	newInChat := source.MemberInChat(&update.NewChatMember)
	if oldInChat == newInChat {
		return domain.SubscriptionEvent{}, false
	}

	kind := domain.EventDeactivated
	if newInChat {
		kind = domain.EventActivated
	}

	occurredAt := time.Now()
	if update.Date > 0 {
		occurredAt = time.Unix(int64(update.Date), 0)
	}

	return domain.SubscriptionEvent{
		Platform:       platform,
		Kind:           kind,
		TGUserID:       user.ID,
		TGUsername:     user.Username,
		TGFirstName:    user.FirstName,
		TGLastName:     user.LastName,
		TGLanguageCode: user.LanguageCode,
		TGIsBot:        user.IsBot,
		OccurredAt:     occurredAt,
	}, true
}

func chatMemberUser(member *models.ChatMember) *models.User {
	if member == nil {
		return nil
	}

	switch member.Type {
	case models.ChatMemberTypeOwner:
		if member.Owner == nil {
			return nil
		}

		return member.Owner.User
	case models.ChatMemberTypeAdministrator:
		if member.Administrator == nil {
			return nil
		}

		return &member.Administrator.User
	case models.ChatMemberTypeMember:
		if member.Member == nil {
			return nil
		}

		return member.Member.User
	case models.ChatMemberTypeRestricted:
		if member.Restricted == nil {
			return nil
		}

		return member.Restricted.User
	case models.ChatMemberTypeLeft:
		if member.Left == nil {
			return nil
		}

		return member.Left.User
	case models.ChatMemberTypeBanned:
		if member.Banned == nil {
			return nil
		}

		return member.Banned.User
	default:
		return nil
	}
}

func fromEngineEffects(effects []engine.Effect) []OutboundMessage {
	out := make([]OutboundMessage, 0, len(effects))
	for _, effect := range effects {
		out = append(out, OutboundMessage{
			Kind: OutboundDM,
			TGID: effect.TGID,
			Text: effect.Text,
		})
	}

	return out
}

func (r *Router) fromCommandResult(
	ctx context.Context,
	result commandbot.Result,
) (RouteResult, error) {
	if result.Ignored {
		return ignored(), nil
	}

	effects := make([]OutboundMessage, 0, len(result.Replies))
	for _, reply := range result.Replies {
		kind := OutboundChatMessage
		if reply.DM {
			kind = OutboundDM
		}

		effects = append(effects, OutboundMessage{
			Kind:   kind,
			ChatID: reply.ChatID,
			TGID:   reply.TGID,
			Text:   reply.Text,
		})
	}

	return r.processedEffects(ctx, effects)
}

func (r *Router) processedEffects(
	ctx context.Context,
	effects []OutboundMessage,
) (RouteResult, error) {
	direct, err := r.durableDMEffects(ctx, effects)
	if err != nil {
		return RouteResult{}, err
	}

	return processed(direct), nil
}

func (r *Router) durableDMEffects(
	ctx context.Context,
	effects []OutboundMessage,
) ([]OutboundMessage, error) {
	if r.outbox == nil {
		return effects, nil
	}

	notifier := notify.NewDurable(r.users, r.outbox, r.logger)
	direct := make([]OutboundMessage, 0, len(effects))

	for i, effect := range effects {
		if effect.Kind != OutboundDM {
			direct = append(direct, effect)

			continue
		}

		marker := fmt.Sprintf("telegram_update:%d:%d", r.updateID, i)
		if err := notifier.SendDurableDM(
			ctx, effect.TGID, effect.Text, marker,
		); err != nil {
			return nil, err
		}
	}

	return direct, nil
}

func processed(effects []OutboundMessage) RouteResult {
	return RouteResult{
		Status:  store.TelegramUpdateProcessed,
		Effects: effects,
	}
}

func ignored() RouteResult {
	return RouteResult{Status: store.TelegramUpdateIgnored}
}
