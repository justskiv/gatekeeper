// Package admission owns the pull-based grant-access flow.
package admission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/engine"
	invitepkg "github.com/justskiv/gatekeeper/internal/invite"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
)

const (
	defaultFallbackMaxAge = time.Hour

	auditAccessRequested      = "access_requested"
	auditAccessDenied         = "access_denied"
	auditJoinApproved         = "join_approved"
	auditJoinDeclined         = "join_declined"
	auditPersonalInviteMisuse = "personal_invite_misuse"
	auditExternalJoinDetected = "external_join_detected"
	auditMemberLeft           = "member_left"

	alertKindSourceUnknown       = "source_status_unknown"
	alertKindInviteMissing       = "invite_link_missing"
	alertKindExternalJoin        = "external_join"
	alertKindDirectStatusMissing = "direct_status_missing"
	alertKindJoinDeclinedActive  = "join_declined_active_sub"
)

var errSharedInviteMissing = errors.New("active shared invite is missing")

// ResourceConfig maps a domain resource to its Telegram chat id.
type ResourceConfig struct {
	Resource domain.Resource
	ChatID   int64
}

// Config controls admission behavior.
type Config struct {
	InviteMode         domain.InviteMode
	Resources          []ResourceConfig
	FallbackMaxAge     time.Duration
	ClubChatID         int64
	ClubChannelID      int64
	JoinRequestRetries int
}

// Deps are transaction-bound admission dependencies.
type Deps struct {
	Users         *store.Users
	Subscriptions *store.Subscriptions
	Grants        *store.Grants
	Invites       *store.Invites
	Outbox        *store.Outbox
	Audit         *store.Audit
	Alerts        *store.Alerts
	Whitelist     *store.Whitelist
	Revocations   *store.Revocations
	StatusEngine  *engine.Engine
	Members       engine.MemberChecker
}

// Handler applies admission decisions inside the handler transaction.
type Handler struct {
	deps Deps
	cfg  Config
	now  func() time.Time
}

// Option configures Handler.
type Option func(*Handler)

// WithClock replaces time.Now for tests.
func WithClock(now func() time.Time) Option {
	return func(h *Handler) {
		h.now = now
	}
}

// New returns a transaction-bound admission handler.
func New(deps Deps, cfg Config, opts ...Option) *Handler {
	if cfg.FallbackMaxAge <= 0 {
		cfg.FallbackMaxAge = defaultFallbackMaxAge
	}

	if len(cfg.Resources) == 0 {
		cfg.Resources = []ResourceConfig{
			{Resource: domain.ResourceChat, ChatID: cfg.ClubChatID},
			{Resource: domain.ResourceChannel, ChatID: cfg.ClubChannelID},
		}
	}

	h := &Handler{
		deps: deps,
		cfg:  cfg,
		now:  time.Now,
	}
	for _, opt := range opts {
		opt(h)
	}

	return h
}

// AccessRequest is a /start, non-command DM, or retry callback.
type AccessRequest struct {
	User     domain.User
	Snapshot *engine.Snapshot
	Trigger  string

	// EditChatID and EditMessageID, when both set, point at the existing
	// message that triggered a retry callback. Reply DMs for this request
	// then edit that message in place instead of sending a new one.
	EditChatID    int64
	EditMessageID int
}

// editTarget returns the in-place edit coordinates for the request and
// whether they are usable. An inaccessible or missing message falls back
// to sending a fresh DM.
func (r AccessRequest) editTarget() (dmEditTarget, bool) {
	if r.EditChatID == 0 || r.EditMessageID == 0 {
		return dmEditTarget{}, false
	}

	return dmEditTarget{ChatID: r.EditChatID, MessageID: r.EditMessageID}, true
}

// HandleAccessRequest processes a user-initiated access request.
func (h *Handler) HandleAccessRequest(
	ctx context.Context,
	req AccessRequest,
) error {
	if h.deps.Users == nil || h.deps.Outbox == nil {
		return errors.New("admission dependencies are incomplete")
	}

	req.User.DMState = domain.DMOpen
	if err := h.deps.Users.Upsert(ctx, req.User); err != nil {
		return err
	}

	user, err := h.deps.Users.Get(ctx, req.User.TGID)
	if err != nil {
		return err
	}

	if user.Banned {
		return h.denyAccessRequest(ctx, req, messages.Banned(), "banned")
	}

	status, fallback, err := h.accessRequestStatus(ctx, req)
	if err != nil {
		return err
	}

	switch status {
	case domain.StatusActive:
		return h.grantAccess(ctx, req, fallback)
	case domain.StatusInactive:
		return h.denyAccessRequest(ctx, req, messages.NoSub(), "inactive")
	default:
		if err := h.alertSourceUnknown(ctx, user.TGID, "access request"); err != nil {
			return err
		}

		return h.denyAccessRequest(ctx, req, messages.TryLater(), "unknown")
	}
}

// JoinRequest is a Telegram chat_join_request for a managed resource.
type JoinRequest struct {
	User        domain.User
	UserChatID  int64
	Resource    domain.Resource
	InviteLink  string
	RequestDate time.Time
	Snapshot    *engine.Snapshot
}

// HandleJoinRequest approves or declines a managed join request.
func (h *Handler) HandleJoinRequest(ctx context.Context, req JoinRequest) error {
	if h.deps.Users == nil || h.deps.Outbox == nil || h.deps.Grants == nil {
		return errors.New("admission dependencies are incomplete")
	}

	if err := h.deps.Users.Upsert(ctx, req.User); err != nil {
		return err
	}

	user, err := h.deps.Users.Get(ctx, req.User.TGID)
	if err != nil {
		return err
	}

	if user.Banned {
		return h.declineJoin(ctx, req, messages.Banned(), "banned")
	}

	resolution, err := h.resolveInvite(ctx, invitepkg.ResolveRequest{
		TGID:       req.User.TGID,
		Resource:   req.Resource,
		Mode:       h.cfg.InviteMode,
		InviteLink: req.InviteLink,
	})
	if err != nil {
		return err
	}

	if resolution.Status == invitepkg.ResolveUsedByOther {
		if err := h.audit(ctx, req.User.TGID, auditPersonalInviteMisuse,
			req.Resource, "personal invite used by another user"); err != nil {
			return err
		}

		return h.declineJoin(ctx, req, messages.PersonalInviteMisused(), "personal_misuse")
	}

	if !resolution.Accepted() {
		if err := h.audit(ctx, req.User.TGID, auditJoinDeclined,
			req.Resource, "invite link was not resolved"); err != nil {
			return err
		}

		return h.declineJoin(ctx, req, messages.InviteNotRecognized(), "invite_unresolved")
	}

	return h.handleResolvedJoin(ctx, req, resolution)
}

func (h *Handler) handleResolvedJoin(
	ctx context.Context,
	req JoinRequest,
	resolution invitepkg.ResolveResult,
) error {
	switch snapshotStatus(req.Snapshot) {
	case domain.StatusActive:
		return h.approveJoin(ctx, req, resolution)
	case domain.StatusInactive:
		return h.declineJoin(ctx, req, messages.NoSub(), "inactive")
	default:
		if err := h.alertSourceUnknown(ctx, req.User.TGID, "join request"); err != nil {
			return err
		}

		return h.declineJoin(ctx, req, messages.TryLater(), "unknown")
	}
}

func (h *Handler) approveJoin(
	ctx context.Context,
	req JoinRequest,
	resolution invitepkg.ResolveResult,
) error {
	if err := h.persistLiveSnapshot(ctx, req.User.TGID, req.Snapshot); err != nil {
		return err
	}

	if err := h.recompute(ctx, req.User.TGID); err != nil {
		return err
	}

	if err := h.deps.Grants.MarkJoinedByAdmission(
		ctx, req.User.TGID, req.Resource, "bot",
	); err != nil {
		return err
	}

	if err := h.markPersonalInviteUsed(ctx, resolution); err != nil {
		return err
	}

	if err := h.enqueueApproveJoin(ctx, req); err != nil {
		return err
	}

	if err := h.audit(ctx, req.User.TGID, auditJoinApproved,
		req.Resource, "live status active"); err != nil {
		return err
	}

	return h.enqueueDM(ctx, dmRequest{
		TGID:   req.User.TGID,
		ChatID: req.UserChatID,
		Text:   messages.Granted(),
		Marker: h.joinMarker(req, "granted"),
	})
}

func (h *Handler) markPersonalInviteUsed(
	ctx context.Context,
	resolution invitepkg.ResolveResult,
) error {
	if resolution.Link == nil ||
		resolution.Link.Mode != domain.InvitePersonalJoinRequest {
		return nil
	}

	return h.inviteService().MarkUsed(ctx, *resolution.Link)
}

// MembershipUpdate is a club chat_member update for a managed resource.
type MembershipUpdate struct {
	User           domain.User
	Resource       domain.Resource
	Joined         bool
	ViaJoinRequest bool
	InviteLink     string
	EventDate      time.Time
	Snapshot       *engine.Snapshot
}

// HandleMembershipUpdate records factual membership in access_grants.
func (h *Handler) HandleMembershipUpdate(
	ctx context.Context,
	update MembershipUpdate,
) error {
	if h.deps.Users == nil || h.deps.Grants == nil {
		return errors.New("admission dependencies are incomplete")
	}

	if err := h.deps.Users.Upsert(ctx, update.User); err != nil {
		return err
	}

	user, err := h.deps.Users.Get(ctx, update.User.TGID)
	if err != nil {
		return err
	}

	if !update.Joined {
		if _, err := h.deps.Grants.MarkLeftUnlessRevoked(
			ctx, update.User.TGID, update.Resource,
		); err != nil {
			return err
		}

		return h.audit(ctx, update.User.TGID, auditMemberLeft,
			update.Resource, "member left managed resource")
	}

	if user.Banned {
		if h.deps.Outbox == nil {
			return errors.New("admission outbox dependency is incomplete")
		}

		if err := h.enqueueHardBan(ctx, update.User.TGID, update.Resource,
			"banned external join", update.EventDate); err != nil {
			return err
		}

		return h.audit(ctx, update.User.TGID, "banned_join_detected",
			update.Resource, "joined while banned")
	}

	botAdmitted, directEvidence, err := h.botAdmissionEvidence(ctx, update)
	if err != nil {
		return err
	}

	admittedBy := "external"
	if botAdmitted {
		admittedBy = "bot"
	}

	if err := h.deps.Grants.MarkJoined(
		ctx, update.User.TGID, update.Resource, admittedBy,
	); err != nil {
		return err
	}

	if !botAdmitted {
		if err := h.audit(ctx, update.User.TGID, auditExternalJoinDetected,
			update.Resource, "member joined without bot admission evidence"); err != nil {
			return err
		}

		return h.alertExternalJoin(ctx, update.User.TGID, update.Resource)
	}

	if directEvidence {
		if update.Snapshot == nil {
			return h.alertDirectStatusMissing(ctx, update.User.TGID, update.Resource)
		}

		if snapshotStatus(update.Snapshot) != domain.StatusActive {
			return h.enqueueSoftKick(ctx, update.User.TGID, update.Resource,
				"direct_join_not_active", update.EventDate)
		}

		if err := h.persistLiveSnapshot(ctx, update.User.TGID, update.Snapshot); err != nil {
			return err
		}

		return h.recompute(ctx, update.User.TGID)
	}

	return nil
}

func (h *Handler) grantAccess(
	ctx context.Context,
	req AccessRequest,
	fallback bool,
) error {
	if err := h.persistLiveSnapshot(ctx, req.User.TGID, req.Snapshot); err != nil {
		return err
	}

	if err := h.recompute(ctx, req.User.TGID); err != nil {
		return err
	}

	missing, err := h.missingResources(ctx, req.User.TGID)
	if err != nil {
		return err
	}

	if len(missing) == 0 {
		return h.enqueueDM(ctx, h.accessResultDM(req, dmRequest{
			TGID:   req.User.TGID,
			Text:   messages.AlreadyIn(h.configuredResources()),
			Marker: h.accessMarker(req.User.TGID, "already-in"),
		}))
	}

	switch h.cfg.InviteMode {
	case domain.InviteSharedJoinRequest:
		links, err := h.sharedLinks(ctx, missing)
		if errors.Is(err, errSharedInviteMissing) {
			if err := h.audit(ctx, req.User.TGID, auditAccessDenied,
				"", "shared_invite_missing"); err != nil {
				return err
			}

			return h.enqueueDM(ctx, h.accessResultDM(req, dmRequest{
				TGID:        req.User.TGID,
				Text:        messages.TryLater(),
				RetryButton: true,
				Marker:      h.accessMarker(req.User.TGID, "shared-missing"),
			}))
		}

		if err != nil {
			return err
		}

		for _, resource := range missing {
			if _, err := h.deps.Grants.MarkPending(
				ctx, req.User.TGID, resource,
			); err != nil {
				return err
			}
		}

		if err := h.auditAccessRequested(ctx, req.User.TGID, fallback); err != nil {
			return err
		}

		return h.enqueueDM(ctx, h.accessResultDM(req, dmRequest{
			TGID:   req.User.TGID,
			Text:   messages.ActiveShared(h.sharedResourceLines(missing, links)),
			Marker: h.accessMarker(req.User.TGID, "active:shared"),
		}))
	case domain.InvitePersonalJoinRequest, domain.InviteDirect:
		for _, resource := range missing {
			pendingAt, err := h.deps.Grants.MarkPending(
				ctx, req.User.TGID, resource,
			)
			if err != nil {
				return err
			}

			if err := h.enqueueSendInvite(
				ctx, req.User.TGID, resource, pendingAt,
			); err != nil {
				return err
			}
		}

		if err := h.auditAccessRequested(ctx, req.User.TGID, fallback); err != nil {
			return err
		}

		text := messages.InviteSoon()
		if h.cfg.InviteMode == domain.InviteDirect {
			text = messages.ActiveDirect()
		}

		return h.enqueueDM(ctx, h.accessResultDM(req, dmRequest{
			TGID: req.User.TGID,
			Text: text,
			Marker: h.accessMarker(req.User.TGID,
				fmt.Sprintf("active:%s", h.cfg.InviteMode)),
		}))
	default:
		return fmt.Errorf("unsupported invite mode %q", h.cfg.InviteMode)
	}
}

// accessResultDM redirects an access-request result reply to an in-place
// message edit when the request carries an edit target (retry callback).
// Access-granting side effects stay durable; only this user-facing reply
// changes delivery shape.
func (h *Handler) accessResultDM(req AccessRequest, dm dmRequest) dmRequest {
	if target, ok := req.editTarget(); ok {
		dm.Edit = &target
	}

	return dm
}

func (h *Handler) denyAccessRequest(
	ctx context.Context,
	req AccessRequest,
	text string,
	reason string,
) error {
	tgID := req.User.TGID
	if err := h.audit(ctx, tgID, auditAccessDenied, "", reason); err != nil {
		return err
	}

	dm := dmRequest{
		TGID:        tgID,
		Text:        text,
		RetryButton: reason == "unknown" || reason == "inactive",
		Marker:      h.accessMarker(tgID, "denied:"+reason),
	}
	if target, ok := req.editTarget(); ok {
		dm.Edit = &target
	}

	return h.enqueueDM(ctx, dm)
}

func (h *Handler) declineJoin(
	ctx context.Context,
	req JoinRequest,
	text string,
	reason string,
) error {
	if err := h.alertDeclinedWithActiveSub(ctx, req, reason); err != nil {
		return err
	}

	if err := h.enqueueDeclineJoin(ctx, req, reason); err != nil {
		return err
	}

	if err := h.audit(ctx, req.User.TGID, auditJoinDeclined, req.Resource, reason); err != nil {
		return err
	}

	return h.enqueueDM(ctx, dmRequest{
		TGID:        req.User.TGID,
		ChatID:      req.UserChatID,
		Text:        text,
		RetryButton: reason == "unknown",
		Marker:      h.joinMarker(req, "declined:"+reason),
	})
}

func (h *Handler) accessRequestStatus(
	ctx context.Context,
	req AccessRequest,
) (domain.EffectiveStatus, bool, error) {
	status := snapshotStatus(req.Snapshot)
	if status != domain.StatusUnknown {
		return status, false, nil
	}

	ok, err := h.hasFreshActiveSubscription(ctx, req.User.TGID)
	if err != nil {
		return "", false, err
	}

	if ok {
		return domain.StatusActive, true, nil
	}

	return domain.StatusUnknown, false, nil
}

func (h *Handler) hasFreshActiveSubscription(
	ctx context.Context,
	tgID int64,
) (bool, error) {
	if h.deps.Subscriptions == nil {
		return false, nil
	}

	subs, err := h.deps.Subscriptions.ListActiveByUser(ctx, tgID)
	if err != nil {
		return false, err
	}

	now := h.now()
	for _, sub := range subs {
		if sub.ExpiresAt != nil && !now.Before(*sub.ExpiresAt) {
			continue
		}

		freshAt := sub.StartedAt
		if sub.LastEventAt != nil && sub.LastEventAt.After(freshAt) {
			freshAt = *sub.LastEventAt
		}

		if sub.LastCheckedAt != nil && sub.LastCheckedAt.After(freshAt) {
			freshAt = *sub.LastCheckedAt
		}

		if now.Sub(freshAt) <= h.cfg.FallbackMaxAge {
			return true, nil
		}
	}

	return false, nil
}

// configuredResources lists managed resources in their configured order.
func (h *Handler) configuredResources() []domain.Resource {
	resources := make([]domain.Resource, 0, len(h.cfg.Resources))
	for _, resource := range h.cfg.Resources {
		resources = append(resources, resource.Resource)
	}

	return resources
}

// sharedResourceLines builds the full ordered resource list for the active
// admission reply: missing resources carry their join link, the rest are
// marked as already joined.
func (h *Handler) sharedResourceLines(
	missing []domain.Resource,
	links []messages.InviteLinkLine,
) []messages.InviteLinkLine {
	urlByResource := make(map[domain.Resource]string, len(links))
	for _, link := range links {
		urlByResource[link.Resource] = link.URL
	}

	missingSet := make(map[domain.Resource]struct{}, len(missing))
	for _, resource := range missing {
		missingSet[resource] = struct{}{}
	}

	lines := make([]messages.InviteLinkLine, 0, len(h.cfg.Resources))
	for _, resource := range h.cfg.Resources {
		if _, ok := missingSet[resource.Resource]; ok {
			lines = append(lines, messages.InviteLinkLine{
				Resource: resource.Resource,
				URL:      urlByResource[resource.Resource],
			})

			continue
		}

		lines = append(lines, messages.InviteLinkLine{
			Resource: resource.Resource,
			Joined:   true,
		})
	}

	return lines
}

func (h *Handler) missingResources(
	ctx context.Context,
	tgID int64,
) ([]domain.Resource, error) {
	grants, err := h.deps.Grants.ListByUser(ctx, tgID)
	if err != nil {
		return nil, err
	}

	joined := make(map[domain.Resource]struct{}, len(grants))

	for _, grant := range grants {
		if grant.State == domain.GrantJoined {
			joined[grant.Resource] = struct{}{}
		}
	}

	var missing []domain.Resource

	for _, resource := range h.cfg.Resources {
		if _, ok := joined[resource.Resource]; ok {
			continue
		}

		missing = append(missing, resource.Resource)
	}

	return missing, nil
}

func (h *Handler) sharedLinks(
	ctx context.Context,
	resources []domain.Resource,
) ([]messages.InviteLinkLine, error) {
	links := make([]messages.InviteLinkLine, 0, len(resources))

	for _, resource := range resources {
		link, ok, err := h.deps.Invites.FindActiveShared(
			ctx, resource, domain.InviteSharedJoinRequest)
		if err != nil {
			return nil, err
		}

		if !ok {
			if err := h.alertInviteMissing(ctx, resource); err != nil {
				return nil, err
			}

			return nil, fmt.Errorf("%w for %s", errSharedInviteMissing, resource)
		}

		links = append(links, messages.InviteLinkLine{
			Resource: resource,
			URL:      link.InviteLink,
		})
	}

	return links, nil
}

func (h *Handler) botAdmissionEvidence(
	ctx context.Context,
	update MembershipUpdate,
) (bool, bool, error) {
	if update.ViaJoinRequest {
		return true, false, nil
	}

	grant, err := h.deps.Grants.Get(ctx, update.User.TGID, update.Resource)
	botGrant := false

	switch {
	case err == nil && grant.AdmittedBy == "bot" &&
		(grant.State == domain.GrantPending || grant.State == domain.GrantJoined):
		botGrant = true
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
	default:
		return false, false, err
	}

	if h.cfg.InviteMode == domain.InviteDirect {
		if botGrant {
			return true, true, nil
		}

		resolution, err := h.resolveInvite(ctx, invitepkg.ResolveRequest{
			TGID:       update.User.TGID,
			Resource:   update.Resource,
			Mode:       domain.InviteDirect,
			InviteLink: update.InviteLink,
		})
		if err != nil {
			return false, false, err
		}

		if resolution.Accepted() {
			return true, true, nil
		}

		return false, false, nil
	}

	if botGrant {
		return true, false, nil
	}

	return false, false, nil
}

func (h *Handler) resolveInvite(
	ctx context.Context,
	req invitepkg.ResolveRequest,
) (invitepkg.ResolveResult, error) {
	if h.deps.Invites == nil {
		return invitepkg.ResolveResult{Status: invitepkg.ResolveNotFound}, nil
	}

	return h.inviteService().ResolveJoinRequest(ctx, req)
}

func (h *Handler) inviteService() *invitepkg.Service {
	return invitepkg.New(nil, h.deps.Invites, invitepkg.Config{
		Mode:          h.cfg.InviteMode,
		ClubChatID:    h.cfg.ClubChatID,
		ClubChannelID: h.cfg.ClubChannelID,
	}, invitepkg.WithClock(h.now))
}

func (h *Handler) persistLiveSnapshot(
	ctx context.Context,
	tgID int64,
	snapshot *engine.Snapshot,
) error {
	if snapshot == nil || h.deps.StatusEngine == nil {
		return nil
	}

	return h.deps.StatusEngine.ApplyObservations(
		ctx, h.engineStore(), tgID, snapshot.Verdicts)
}

func (h *Handler) recompute(ctx context.Context, tgID int64) error {
	if h.deps.StatusEngine == nil || h.deps.Revocations == nil {
		return nil
	}

	effects, err := h.deps.StatusEngine.RecomputeAccess(ctx, h.engineStore(), tgID)
	if err != nil {
		return err
	}

	for _, effect := range effects {
		if err := h.enqueueDM(ctx, dmRequest{
			TGID:   effect.TGID,
			Text:   effect.Text,
			Marker: fmt.Sprintf("recompute:%d:%s", effect.TGID, effect.Text),
		}); err != nil {
			return err
		}
	}

	return nil
}

func (h *Handler) engineStore() engine.Store {
	return engine.Store{
		Users:         h.deps.Users,
		Subscriptions: h.deps.Subscriptions,
		Audit:         h.deps.Audit,
		Revocations:   h.deps.Revocations,
		Whitelist:     h.deps.Whitelist,
		Grants:        h.deps.Grants,
		Outbox:        h.deps.Outbox,
		Alerts:        h.deps.Alerts,
		Members:       h.deps.Members,
	}
}

func (h *Handler) enqueueDM(ctx context.Context, req dmRequest) error {
	if req.Edit != nil {
		return h.enqueueEditMessage(ctx, req)
	}

	payload, err := json.Marshal(sendDMPayload{
		Text:        req.Text,
		ParseMode:   messages.ParseModeHTML,
		ChatID:      req.ChatID,
		RetryButton: req.RetryButton,
	})
	if err != nil {
		return fmt.Errorf("encode dm payload: %w", err)
	}

	_, _, err = h.deps.Outbox.Enqueue(ctx, store.AccessActionInput{
		Type: domain.ActionSendDM,
		TGID: &req.TGID,
		IdempotencyKey: domain.AccessActionKey(
			domain.ActionSendDM, &req.TGID, nil, req.Marker),
		PayloadJSON: payload,
	})
	if err != nil {
		return fmt.Errorf("enqueue dm: %w", err)
	}

	return nil
}

func (h *Handler) enqueueEditMessage(ctx context.Context, req dmRequest) error {
	payload, err := json.Marshal(editMessagePayload{
		ChatID:      req.Edit.ChatID,
		MessageID:   req.Edit.MessageID,
		Text:        req.Text,
		RetryButton: req.RetryButton,
	})
	if err != nil {
		return fmt.Errorf("encode edit message payload: %w", err)
	}

	_, _, err = h.deps.Outbox.Enqueue(ctx, store.AccessActionInput{
		Type: domain.ActionEditMessage,
		TGID: &req.TGID,
		IdempotencyKey: domain.AccessActionKey(
			domain.ActionEditMessage, &req.TGID, nil, req.Marker),
		PayloadJSON: payload,
	})
	if err != nil {
		return fmt.Errorf("enqueue edit message: %w", err)
	}

	return nil
}

func (h *Handler) enqueueSendInvite(
	ctx context.Context,
	tgID int64,
	resource domain.Resource,
	pendingAt time.Time,
) error {
	marker := fmt.Sprintf("grant-access:%s:%s",
		h.cfg.InviteMode, pendingAt.UTC().Format(time.RFC3339Nano))

	_, _, err := h.deps.Outbox.Enqueue(ctx, store.AccessActionInput{
		Type:     domain.ActionSendInvite,
		TGID:     &tgID,
		Resource: &resource,
		IdempotencyKey: domain.AccessActionKey(
			domain.ActionSendInvite, &tgID, &resource, marker),
		PayloadJSON: []byte(`{}`),
	})

	return err
}

func (h *Handler) enqueueApproveJoin(ctx context.Context, req JoinRequest) error {
	_, _, err := h.deps.Outbox.Enqueue(ctx, store.AccessActionInput{
		Type:     domain.ActionApproveJoin,
		TGID:     &req.User.TGID,
		Resource: &req.Resource,
		IdempotencyKey: domain.AccessActionKey(
			domain.ActionApproveJoin,
			&req.User.TGID,
			&req.Resource,
			h.joinMarker(req, "approve"),
		),
		PayloadJSON: []byte(`{}`),
	})

	return err
}

func (h *Handler) enqueueDeclineJoin(
	ctx context.Context,
	req JoinRequest,
	reason string,
) error {
	_, _, err := h.deps.Outbox.Enqueue(ctx, store.AccessActionInput{
		Type:     domain.ActionDeclineJoin,
		TGID:     &req.User.TGID,
		Resource: &req.Resource,
		IdempotencyKey: domain.AccessActionKey(
			domain.ActionDeclineJoin,
			&req.User.TGID,
			&req.Resource,
			h.joinMarker(req, "decline:"+reason),
		),
		PayloadJSON: []byte(`{}`),
	})

	return err
}

func (h *Handler) enqueueSoftKick(
	ctx context.Context,
	tgID int64,
	resource domain.Resource,
	reason string,
	eventDate time.Time,
) error {
	if eventDate.IsZero() {
		eventDate = h.now()
	}

	marker := fmt.Sprintf("%s:%s", reason, eventDate.UTC().Format(time.RFC3339Nano))

	_, _, err := h.deps.Outbox.Enqueue(ctx, store.AccessActionInput{
		Type:     domain.ActionSoftKick,
		TGID:     &tgID,
		Resource: &resource,
		IdempotencyKey: domain.AccessActionKey(
			domain.ActionSoftKick, &tgID, &resource, marker),
		PayloadJSON: []byte(`{}`),
	})

	return err
}

func (h *Handler) enqueueHardBan(
	ctx context.Context,
	tgID int64,
	resource domain.Resource,
	reason string,
	eventDate time.Time,
) error {
	if eventDate.IsZero() {
		eventDate = h.now()
	}

	payload, err := json.Marshal(struct {
		Reason string `json:"reason,omitempty"`
	}{Reason: reason})
	if err != nil {
		return err
	}

	_, _, err = h.deps.Outbox.Enqueue(ctx, store.AccessActionInput{
		Type:     domain.ActionHardBan,
		TGID:     &tgID,
		Resource: &resource,
		IdempotencyKey: domain.AccessActionKey(
			domain.ActionHardBan,
			&tgID,
			&resource,
			"banned_join:"+eventDate.UTC().Format(time.RFC3339Nano),
		),
		PayloadJSON: payload,
	})

	return err
}

func (h *Handler) auditAccessRequested(
	ctx context.Context,
	tgID int64,
	fallback bool,
) error {
	detail := "live status active"
	if fallback {
		detail = "fresh local active subscription fallback"
	}

	return h.audit(ctx, tgID, auditAccessRequested, "", detail)
}

func (h *Handler) audit(
	ctx context.Context,
	tgID int64,
	kind string,
	resource domain.Resource,
	detail string,
) error {
	if h.deps.Audit == nil {
		return nil
	}

	entry := store.AuditEntry{
		TGID:   &tgID,
		Kind:   kind,
		Actor:  "system",
		Detail: detail,
	}
	if resource != "" {
		entry.Resource = string(resource)
	}

	return h.deps.Audit.Append(ctx, entry)
}

func (h *Handler) alertSourceUnknown(
	ctx context.Context,
	tgID int64,
	place string,
) error {
	if h.deps.Alerts == nil {
		return nil
	}

	_, _, err := h.deps.Alerts.CreateOpenIfMissing(ctx, store.AlertInput{
		Severity: "warning",
		Kind:     alertKindSourceUnknown,
		Title:    "source status unknown for " + place,
		Detail:   fmt.Sprintf("tg_id=%d", tgID),
		TGID:     &tgID,
	})

	return err
}

// alertDeclinedWithActiveSub raises an admin alert when a join request is
// declined for a user who still has an active subscription. That combination
// is anomalous: an eligible user being turned away usually means a broken
// invite link or a logic bug (this is how the recent silent redaction bug
// went unnoticed). Normal "no subscription" declines (inactive) are expected
// and never alert here.
func (h *Handler) alertDeclinedWithActiveSub(
	ctx context.Context,
	req JoinRequest,
	reason string,
) error {
	if h.deps.Alerts == nil {
		return nil
	}

	if reason != "invite_unresolved" {
		return nil
	}

	active, err := h.hasFreshActiveSubscription(ctx, req.User.TGID)
	if err != nil {
		return err
	}

	if !active {
		return nil
	}

	tgID := req.User.TGID

	_, _, err = h.deps.Alerts.CreateOpenIfMissing(ctx, store.AlertInput{
		Severity: "warning",
		Kind:     alertKindJoinDeclinedActive,
		Title: fmt.Sprintf(
			"join declined with active subscription %s/%d", req.Resource, tgID),
		Detail: fmt.Sprintf("reason=%s resource=%s tg_id=%d",
			reason, req.Resource, tgID),
		TGID: &tgID,
	})

	return err
}

func (h *Handler) alertInviteMissing(
	ctx context.Context,
	resource domain.Resource,
) error {
	if h.deps.Alerts == nil {
		return nil
	}

	_, _, err := h.deps.Alerts.CreateOpenIfMissing(ctx, store.AlertInput{
		Severity: "error",
		Kind:     alertKindInviteMissing,
		Title:    fmt.Sprintf("active shared invite missing for %s", resource),
		Detail:   fmt.Sprintf("resource=%s", resource),
	})

	return err
}

func (h *Handler) alertExternalJoin(
	ctx context.Context,
	tgID int64,
	resource domain.Resource,
) error {
	if h.deps.Alerts == nil {
		return nil
	}

	_, _, err := h.deps.Alerts.CreateOpenIfMissing(ctx, store.AlertInput{
		Severity: "info",
		Kind:     alertKindExternalJoin,
		Title:    fmt.Sprintf("external join %s/%d", resource, tgID),
		Detail:   fmt.Sprintf("resource=%s tg_id=%d", resource, tgID),
		TGID:     &tgID,
	})

	return err
}

func (h *Handler) alertDirectStatusMissing(
	ctx context.Context,
	tgID int64,
	resource domain.Resource,
) error {
	if h.deps.Alerts == nil {
		return nil
	}

	_, _, err := h.deps.Alerts.CreateOpenIfMissing(ctx, store.AlertInput{
		Severity: "warning",
		Kind:     alertKindDirectStatusMissing,
		Title:    fmt.Sprintf("direct join status missing %s/%d", resource, tgID),
		Detail:   fmt.Sprintf("resource=%s tg_id=%d", resource, tgID),
		TGID:     &tgID,
	})

	return err
}

func (h *Handler) joinMarker(req JoinRequest, suffix string) string {
	date := req.RequestDate
	if date.IsZero() {
		date = h.now()
	}

	return fmt.Sprintf("join:%s:%d:%d:%s",
		req.Resource, req.User.TGID, date.Unix(), suffix)
}

func (h *Handler) accessMarker(tgID int64, label string) string {
	// Unique per call so every access request gets a reply. The old 30s
	// time-bucket made repeated /start share one idempotency key, and the
	// outbox silently swallowed the duplicates. Dedup here is dropped until
	// the in-memory decision cache replaces it; action markers keep theirs.
	return fmt.Sprintf("access:%s:%d:%d", label, tgID, h.now().UnixNano())
}

func snapshotStatus(snapshot *engine.Snapshot) domain.EffectiveStatus {
	if snapshot == nil {
		return domain.StatusUnknown
	}

	return snapshot.Decision.Status
}

type dmRequest struct {
	TGID        int64
	ChatID      int64
	Text        string
	RetryButton bool
	Marker      string

	// Edit, when set, redirects this reply to an in-place message edit
	// instead of a fresh DM. Used for the retry-access callback so the
	// result lands on the same message the button was attached to.
	Edit *dmEditTarget
}

type dmEditTarget struct {
	ChatID    int64
	MessageID int
}

type sendDMPayload struct {
	Text        string `json:"text"`
	ParseMode   string `json:"parse_mode,omitempty"`
	Plain       bool   `json:"plain,omitempty"`
	ChatID      int64  `json:"chat_id,omitempty"`
	RetryButton bool   `json:"retry_button,omitempty"`
}

type editMessagePayload struct {
	ChatID      int64  `json:"chat_id"`
	MessageID   int    `json:"message_id"`
	Text        string `json:"text"`
	RetryButton bool   `json:"retry_button,omitempty"`
}
