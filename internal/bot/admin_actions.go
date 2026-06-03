package bot

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
)

const (
	adminConfirmPrefix = "admin.confirm."
	adminCancelPrefix  = "admin.cancel."
	confirmationTTL    = 5 * time.Minute
)

// AdminSyncFunc runs reconciliation from an owner command.
type AdminSyncFunc func(ctx context.Context, tgID *int64) (string, error)

type adminAction struct {
	ID        string
	OwnerID   int64
	Kind      string
	TargetID  *int64
	Reason    string
	ExpiresAt *time.Time
	CreatedAt time.Time
	Executed  bool
	Executing bool
	Result    string
}

var adminConfirmations = struct {
	sync.Mutex

	items map[string]*adminAction
}{items: map[string]*adminAction{}}

func (h *UserCommands) handleAdminAction(
	ctx context.Context,
	msg *models.Message,
	kind string,
) (Result, error) {
	if !h.isOwner(msg.From.ID) {
		return Result{Ignored: true}, nil
	}

	action, err := h.parseAdminAction(ctx, msg, kind)
	if err != nil {
		return Result{}, err
	}

	if action == nil {
		return h.ownerReply(msg, messages.AdminCommandUsage(kind)), nil
	}

	actionID, err := newActionID()
	if err != nil {
		return Result{}, err
	}

	action.ID = actionID
	action.OwnerID = msg.From.ID
	action.CreatedAt = time.Now()

	adminConfirmations.Lock()
	adminConfirmations.items[action.ID] = action
	adminConfirmations.Unlock()

	return Result{Replies: []Reply{{
		ChatID:    msg.Chat.ID,
		TGID:      msg.From.ID,
		Text:      messages.AdminConfirm(actionSummary(*action)),
		ParseMode: messages.ParseModeHTML,
		DM:        true,
		Buttons: [][]Button{{
			{
				Text:         messages.AdminConfirmButtonText,
				CallbackData: adminConfirmPrefix + action.ID,
			},
			{
				Text:         messages.AdminCancelButtonText,
				CallbackData: adminCancelPrefix + action.ID,
			},
		}},
	}}}, nil
}

func (h *UserCommands) handleAdminCallback(
	ctx context.Context,
	query *models.CallbackQuery,
) (Result, error) {
	if query == nil {
		return Result{Ignored: true}, nil
	}

	confirm := strings.HasPrefix(query.Data, adminConfirmPrefix)
	cancel := strings.HasPrefix(query.Data, adminCancelPrefix)

	if !confirm && !cancel {
		return Result{Ignored: true}, nil
	}

	if !h.isOwner(query.From.ID) {
		return Result{Ignored: true}, nil
	}

	now := time.Now()

	id := strings.TrimPrefix(query.Data, adminConfirmPrefix)
	if cancel {
		id = strings.TrimPrefix(query.Data, adminCancelPrefix)
	}

	adminConfirmations.Lock()
	sweepAdminConfirmationsLocked(now)

	action := adminConfirmations.items[id]

	if action == nil || action.OwnerID != query.From.ID {
		adminConfirmations.Unlock()

		return h.callbackReply(query, messages.AdminConfirmationExpired()), nil
	}

	if action.Executed {
		result := action.Result
		adminConfirmations.Unlock()

		return h.callbackReply(query, result), nil
	}

	if action.Executing {
		adminConfirmations.Unlock()

		return h.callbackReply(query, messages.AdminConfirmationInProgress()), nil
	}

	if now.Sub(action.CreatedAt) > confirmationTTL {
		action.Executed = true
		action.Result = messages.AdminConfirmationExpired()
		result := action.Result
		adminConfirmations.Unlock()

		return h.callbackReply(query, result), nil
	}

	if cancel {
		action.Executed = true
		action.Result = messages.AdminCancelled()
		result := action.Result
		adminConfirmations.Unlock()

		return h.callbackReply(query, result), nil
	}

	action.Executing = true
	adminConfirmations.Unlock()

	result, err := h.executeAdminAction(ctx, action)
	if err != nil {
		adminConfirmations.Lock()
		action.Executing = false
		adminConfirmations.Unlock()

		return Result{}, err
	}

	adminConfirmations.Lock()
	action.Executing = false
	action.Executed = true
	action.Result = messages.AdminConfirmed(result)
	final := action.Result
	adminConfirmations.Unlock()

	return h.callbackReply(query, final), nil
}

func sweepAdminConfirmationsLocked(now time.Time) {
	for id, action := range adminConfirmations.items {
		if action == nil {
			delete(adminConfirmations.items, id)

			continue
		}

		if !action.Executed && now.Sub(action.CreatedAt) <= 2*confirmationTTL {
			continue
		}

		if now.Sub(action.CreatedAt) > 2*confirmationTTL {
			delete(adminConfirmations.items, id)
		}
	}
}

func (h *UserCommands) parseAdminAction(
	ctx context.Context,
	msg *models.Message,
	kind string,
) (*adminAction, error) {
	fields := strings.Fields(strings.TrimSpace(msg.Text))
	if len(fields) < 2 && kind != "sync" {
		return nil, nil
	}

	action := &adminAction{Kind: kind}

	if kind == "sync" && len(fields) == 1 {
		return action, nil
	}

	target, ok, err := h.resolveAdminTarget(ctx, fields[1])
	if err != nil || !ok {
		return nil, err
	}

	action.TargetID = &target

	rest := fields[2:]
	if kind == "grant" && len(rest) > 0 {
		if d, err := time.ParseDuration(rest[0]); err == nil {
			expiresAt := time.Now().Add(d)
			action.ExpiresAt = &expiresAt
			rest = rest[1:]
		}
	}

	action.Reason = strings.Join(rest, " ")

	return action, nil
}

func (h *UserCommands) resolveAdminTarget(
	ctx context.Context,
	raw string,
) (int64, bool, error) {
	if strings.HasPrefix(raw, "@") {
		if h.deps.Users == nil {
			return 0, false, nil
		}

		user, ok, err := h.deps.Users.FindByUsername(ctx, raw)
		if err != nil || !ok {
			return 0, ok, err
		}

		return user.TGID, true, nil
	}

	tgID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || tgID <= 0 {
		return 0, false, nil
	}

	return tgID, true, nil
}

func (h *UserCommands) executeAdminAction(
	ctx context.Context,
	action *adminAction,
) (string, error) {
	switch action.Kind {
	case "grant":
		return h.executeGrant(ctx, action)
	case "revoke":
		return h.executeRevoke(ctx, action)
	case "ban":
		return h.executeBan(ctx, action)
	case "unban":
		return h.executeUnban(ctx, action)
	case "sync":
		return h.executeSync(ctx, action)
	default:
		return "", fmt.Errorf("unknown admin action %q", action.Kind)
	}
}

func (h *UserCommands) executeGrant(
	ctx context.Context,
	action *adminAction,
) (string, error) {
	tgID := *action.TargetID
	if err := h.deps.Users.EnsureStub(ctx, tgID); err != nil {
		return "", err
	}

	reason := action.Reason
	if reason == "" {
		reason = "manual grant"
	}

	if action.ExpiresAt == nil {
		if err := h.deps.Whitelist.Add(ctx, tgID, action.OwnerID, reason); err != nil {
			return "", err
		}
	} else if _, err := h.deps.Subscriptions.UpsertManual(
		ctx, tgID, action.ExpiresAt, "manual",
	); err != nil {
		return "", err
	}

	if err := h.deps.Audit.Append(ctx, store.AuditEntry{
		TGID:   &tgID,
		Kind:   "manual_grant",
		Actor:  "admin",
		Detail: reason,
	}); err != nil {
		return "", err
	}

	if err := h.recomputeForAdmin(ctx, tgID); err != nil {
		return "", err
	}

	return fmt.Sprintf("/grant %d", tgID), nil
}

func (h *UserCommands) executeRevoke(
	ctx context.Context,
	action *adminAction,
) (string, error) {
	tgID := *action.TargetID
	reason := action.Reason

	if reason == "" {
		reason = "manual revoke"
	}

	if err := h.deps.Whitelist.Remove(ctx, tgID); err != nil {
		return "", err
	}

	if _, err := h.deps.Subscriptions.ExpireManual(
		ctx, tgID, time.Now(), "manual",
	); err != nil {
		return "", err
	}

	if err := h.deps.Audit.Append(ctx, store.AuditEntry{
		TGID:   &tgID,
		Kind:   "manual_revoke",
		Actor:  "admin",
		Detail: reason,
	}); err != nil {
		return "", err
	}

	if err := h.recomputeForAdmin(ctx, tgID); err != nil {
		return "", err
	}

	return fmt.Sprintf("/revoke %d", tgID), nil
}

func (h *UserCommands) executeBan(
	ctx context.Context,
	action *adminAction,
) (string, error) {
	tgID := *action.TargetID
	reason := action.Reason

	if reason == "" {
		reason = "manual ban"
	}

	if err := h.deps.Users.EnsureStub(ctx, tgID); err != nil {
		return "", err
	}

	if err := h.deps.Users.SetBanned(ctx, tgID, true, reason); err != nil {
		return "", err
	}

	if h.deps.Revocations != nil {
		if err := h.deps.Revocations.Delete(ctx, tgID); err != nil {
			return "", err
		}
	}

	for _, resource := range []domain.Resource{
		domain.ResourceChat,
		domain.ResourceChannel,
	} {
		if err := h.enqueueMembershipAction(
			ctx, domain.ActionHardBan, tgID, resource, reason,
		); err != nil {
			return "", err
		}

		if _, err := h.deps.Grants.Revoke(ctx, tgID, resource, reason); err != nil {
			return "", err
		}
	}

	if err := h.deps.Audit.Append(ctx, store.AuditEntry{
		TGID:   &tgID,
		Kind:   "manual_ban",
		Actor:  "admin",
		Detail: reason,
	}); err != nil {
		return "", err
	}

	return fmt.Sprintf("/ban %d", tgID), nil
}

func (h *UserCommands) executeUnban(
	ctx context.Context,
	action *adminAction,
) (string, error) {
	tgID := *action.TargetID
	reason := action.Reason

	if reason == "" {
		reason = "manual unban"
	}

	if err := h.deps.Users.SetBanned(ctx, tgID, false, reason); err != nil {
		return "", err
	}

	for _, resource := range []domain.Resource{
		domain.ResourceChat,
		domain.ResourceChannel,
	} {
		if err := h.enqueueMembershipAction(
			ctx, domain.ActionUnban, tgID, resource, reason,
		); err != nil {
			return "", err
		}
	}

	if err := h.deps.Audit.Append(ctx, store.AuditEntry{
		TGID:   &tgID,
		Kind:   "manual_unban",
		Actor:  "admin",
		Detail: reason,
	}); err != nil {
		return "", err
	}

	return fmt.Sprintf("/unban %d", tgID), nil
}

func (h *UserCommands) executeSync(
	ctx context.Context,
	action *adminAction,
) (string, error) {
	if h.deps.AdminSync == nil {
		return messages.AdminSyncUnavailable(), nil
	}

	return h.deps.AdminSync(ctx, action.TargetID)
}

func (h *UserCommands) recomputeForAdmin(ctx context.Context, tgID int64) error {
	if h.deps.StatusEngine == nil {
		return nil
	}

	effects, err := h.deps.StatusEngine.RecomputeAccess(ctx, engineStore(h.deps), tgID)
	if err != nil {
		return err
	}

	for _, effect := range effects {
		if err := h.enqueueUserDM(ctx, effect.TGID, effect.Text,
			fmt.Sprintf("admin-recompute:%d:%s", effect.TGID, effect.Text)); err != nil {
			return err
		}
	}

	return nil
}

func (h *UserCommands) enqueueUserDM(
	ctx context.Context,
	tgID int64,
	text string,
	marker string,
) error {
	payload, err := json.Marshal(struct {
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode,omitempty"`
	}{Text: text, ParseMode: messages.ParseModeHTML})
	if err != nil {
		return err
	}

	_, _, err = h.deps.Outbox.Enqueue(ctx, store.AccessActionInput{
		Type: domain.ActionSendDM,
		TGID: &tgID,
		IdempotencyKey: domain.AccessActionKey(
			domain.ActionSendDM, &tgID, nil, marker),
		PayloadJSON: payload,
	})

	return err
}

func (h *UserCommands) enqueueMembershipAction(
	ctx context.Context,
	actionType domain.ActionType,
	tgID int64,
	resource domain.Resource,
	reason string,
) error {
	payload, err := json.Marshal(struct {
		Reason string `json:"reason,omitempty"`
	}{Reason: reason})
	if err != nil {
		return err
	}

	_, _, err = h.deps.Outbox.Enqueue(ctx, store.AccessActionInput{
		Type:     actionType,
		TGID:     &tgID,
		Resource: &resource,
		IdempotencyKey: domain.AccessActionKey(
			actionType, &tgID, &resource, "admin:"+reason),
		PayloadJSON: payload,
	})

	return err
}

func (h *UserCommands) callbackReply(query *models.CallbackQuery, text string) Result {
	chatID := query.From.ID
	if query.Message.Message != nil {
		chatID = query.Message.Message.Chat.ID
	} else if query.Message.InaccessibleMessage != nil {
		chatID = query.Message.InaccessibleMessage.Chat.ID
	}

	return Result{Replies: []Reply{{
		ChatID:    chatID,
		TGID:      query.From.ID,
		Text:      text,
		ParseMode: messages.ParseModeHTML,
		DM:        true,
	}}}
}

func actionSummary(action adminAction) string {
	target := messages.AdminActionAllUsers()
	if action.TargetID != nil {
		target = messages.AdminActionTargetID(*action.TargetID)
	}

	var b strings.Builder
	b.WriteString(messages.AdminActionSummary(action.Kind, target))

	if action.ExpiresAt != nil {
		fmt.Fprintf(&b, "\n%s", messages.AdminActionExpiryLine(*action.ExpiresAt))
	}

	if action.Reason != "" {
		fmt.Fprintf(&b, "\n%s", messages.AdminActionReasonLine(action.Reason))
	}

	return b.String()
}

func newActionID() (string, error) {
	var raw [9]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate admin action id: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// IsAdminCallback reports whether callback data belongs to owner commands.
func IsAdminCallback(data string) bool {
	return strings.HasPrefix(data, adminConfirmPrefix) ||
		strings.HasPrefix(data, adminCancelPrefix)
}
