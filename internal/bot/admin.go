package bot

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
)

const whoisAuditLimit = 8

// WhoisTarget is a resolved /whois argument.
type WhoisTarget struct {
	TGID      int64
	Query     string
	NotFound  bool
	BadSyntax bool
}

// ResolveWhoisTarget resolves a /whois argument using only local data.
func ResolveWhoisTarget(
	ctx context.Context,
	users *store.Users,
	text string,
) (WhoisTarget, error) {
	arg := commandArg(text)
	if arg == "" {
		return WhoisTarget{BadSyntax: true}, nil
	}
	if strings.HasPrefix(arg, "@") {
		if users == nil {
			return WhoisTarget{Query: arg, NotFound: true}, nil
		}
		user, ok, err := users.FindByUsername(ctx, arg)
		if err != nil {
			return WhoisTarget{}, err
		}
		if !ok {
			return WhoisTarget{Query: arg, NotFound: true}, nil
		}
		return WhoisTarget{TGID: user.TGID, Query: arg}, nil
	}
	tgID, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || tgID <= 0 {
		return WhoisTarget{Query: arg, BadSyntax: true}, nil
	}
	return WhoisTarget{TGID: tgID, Query: arg}, nil
}

func (h *UserCommands) handleWhois(
	ctx context.Context, msg *models.Message,
) (Result, error) {
	if !h.isOwner(msg.From.ID) {
		return Result{Ignored: true}, nil
	}
	if !h.hasWhoisDeps() {
		return h.ownerReply(msg, messages.WhoisUnavailable()), nil
	}

	target, err := ResolveWhoisTarget(ctx, h.deps.Users, msg.Text)
	if err != nil {
		return Result{}, err
	}
	if target.BadSyntax {
		return h.ownerReply(msg, messages.WhoisUsage()), nil
	}
	if target.NotFound {
		return h.ownerReply(msg, messages.WhoisNotFound(target.Query)), nil
	}

	user, err := h.deps.Users.Get(ctx, target.TGID)
	if isNotFound(err) {
		return h.ownerReply(msg, messages.WhoisNotFound(target.Query)), nil
	}
	if err != nil {
		return Result{}, err
	}
	if h.deps.StatusEngine != nil &&
		h.deps.Preflight != nil &&
		h.deps.Preflight.TGID == target.TGID {
		if err := h.deps.StatusEngine.ApplyObservations(
			ctx, engineStore(h.deps), target.TGID, h.deps.Preflight.Verdicts,
		); err != nil {
			return Result{}, err
		}
	}

	subs, err := h.deps.Subscriptions.ListByUser(ctx, target.TGID)
	if err != nil {
		return Result{}, err
	}
	grants, err := h.deps.Grants.ListByUser(ctx, target.TGID)
	if err != nil {
		return Result{}, err
	}
	whitelisted, err := h.deps.Whitelist.Has(ctx, target.TGID)
	if err != nil {
		return Result{}, err
	}
	auditRows, err := h.deps.Audit.ListRecentByUser(ctx, target.TGID, whoisAuditLimit)
	if err != nil {
		return Result{}, err
	}

	decision, err := h.statusDecision(ctx, target.TGID)
	if err != nil {
		return Result{}, err
	}
	audit := make([]messages.AuditLine, 0, len(auditRows))
	for _, row := range auditRows {
		audit = append(audit, messages.AuditLine{
			Kind:      row.Kind,
			Source:    row.Source,
			Detail:    row.Detail,
			CreatedAt: row.CreatedAt,
		})
	}
	return h.ownerReply(msg, messages.Whois(messages.WhoisData{
		User:          user,
		Subscriptions: subs,
		Grants:        grants,
		Whitelisted:   whitelisted,
		Decision:      decision,
		Audit:         audit,
	})), nil
}

func (h *UserCommands) hasWhoisDeps() bool {
	return h.deps.Users != nil &&
		h.deps.Subscriptions != nil &&
		h.deps.Grants != nil &&
		h.deps.Audit != nil &&
		h.deps.Whitelist != nil
}

func (h *UserCommands) ownerReply(msg *models.Message, text string) Result {
	return Result{Replies: []Reply{{
		ChatID: msg.Chat.ID,
		TGID:   msg.From.ID,
		Text:   text,
		DM:     true,
	}}}
}

func commandArg(text string) string {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
}
