package invite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/store"
)

const defaultPersonalTTL = 24 * time.Hour
const maxTelegramInviteNameLen = 32

// Config describes invite-link behavior for managed resources.
type Config struct {
	Mode          domain.InviteMode
	TTL           time.Duration
	ClubChatID    int64
	ClubChannelID int64
}

// EnsureRequest asks Service to return a usable invite link.
type EnsureRequest struct {
	TGID     *int64
	Resource domain.Resource
}

// ResolveRequest describes a Telegram join request invite-link signal.
type ResolveRequest struct {
	TGID       int64
	Resource   domain.Resource
	Mode       domain.InviteMode
	InviteLink string
}

// ResolveStatus describes how a join request invite was resolved.
type ResolveStatus string

const (
	ResolveSharedAccepted   ResolveStatus = "shared_accepted"
	ResolvePersonalAccepted ResolveStatus = "personal_accepted"
	ResolveDirectAccepted   ResolveStatus = "direct_accepted"
	ResolveSafeFallback     ResolveStatus = "safe_fallback"
	ResolveUsedByOther      ResolveStatus = "used_by_other"
	ResolveNotFound         ResolveStatus = "not_found"
)

// ResolveResult is the outcome of invite-link resolution for admission.
type ResolveResult struct {
	Status ResolveStatus
	Link   *domain.InviteLink
}

// Accepted reports whether the resolution allows eligibility checks to proceed.
func (r ResolveResult) Accepted() bool {
	switch r.Status {
	case ResolveSharedAccepted, ResolvePersonalAccepted,
		ResolveDirectAccepted, ResolveSafeFallback:
		return true
	default:
		return false
	}
}

// Service manages invite_links rows and Telegram invite-link creation.
type Service struct {
	links  LinkManager
	store  Store
	cfg    Config
	logger *slog.Logger
	now    func() time.Time
	nonce  func() (string, error)
}

// Option configures Service.
type Option func(*Service)

// WithLogger sets the service logger.
func WithLogger(logger *slog.Logger) Option {
	return func(s *Service) {
		if logger != nil {
			s.logger = logger
		}
	}
}

// WithClock replaces time.Now for tests.
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		s.now = now
	}
}

// WithNonce replaces nonce generation for tests.
func WithNonce(nonce func() (string, error)) Option {
	return func(s *Service) {
		s.nonce = nonce
	}
}

// New returns an invite-link service.
func New(links LinkManager, store Store, cfg Config, opts ...Option) *Service {
	if cfg.TTL <= 0 {
		cfg.TTL = defaultPersonalTTL
	}

	s := &Service{
		links:  links,
		store:  store,
		cfg:    cfg,
		logger: slog.Default(),
		now:    time.Now,
		nonce:  randomNonce,
	}
	for _, opt := range opts {
		opt(s)
	}

	return s
}

// Ensure returns an active link for the configured mode.
func (s *Service) Ensure(
	ctx context.Context,
	req EnsureRequest,
) (domain.InviteLink, error) {
	switch s.cfg.Mode {
	case domain.InviteSharedJoinRequest:
		return s.ensureShared(ctx, req.Resource)
	case domain.InvitePersonalJoinRequest:
		return s.ensurePersonal(ctx, req)
	case domain.InviteDirect:
		return s.ensureDirect(ctx, req)
	default:
		return domain.InviteLink{},
			fmt.Errorf("unsupported invite mode %q", s.cfg.Mode)
	}
}

// ActiveShared returns the active shared link for startup readiness checks.
func (s *Service) ActiveShared(
	ctx context.Context,
	resource domain.Resource,
) (domain.InviteLink, bool, error) {
	return s.store.FindActiveShared(ctx, resource, domain.InviteSharedJoinRequest)
}

// ResolveJoinRequest resolves Telegram invite data without exposing full URLs.
func (s *Service) ResolveJoinRequest(
	ctx context.Context,
	req ResolveRequest,
) (ResolveResult, error) {
	if s.store == nil {
		return ResolveResult{}, errors.New("invite store is nil")
	}

	if req.InviteLink == "" {
		return s.resolveMissingInviteLink(ctx, req)
	}

	hash := HashInviteLink(req.InviteLink)

	link, ok, err := s.store.FindActiveByHash(ctx, req.Resource, hash)
	if err != nil || !ok {
		return ResolveResult{Status: ResolveNotFound}, err
	}

	if ok, err := s.ensureLinkUsable(ctx, link); err != nil || !ok {
		return ResolveResult{Status: ResolveNotFound}, err
	}

	switch link.Mode {
	case domain.InviteSharedJoinRequest:
		if req.Mode == domain.InviteSharedJoinRequest {
			return ResolveResult{Status: ResolveSharedAccepted, Link: &link}, nil
		}
	case domain.InvitePersonalJoinRequest:
		if req.Mode != domain.InvitePersonalJoinRequest {
			break
		}

		return s.resolveOwnedInvite(ctx, req, link, ResolvePersonalAccepted)
	case domain.InviteDirect:
		if req.Mode != domain.InviteDirect {
			break
		}

		return s.resolveOwnedInvite(ctx, req, link, ResolveDirectAccepted)
	}

	return ResolveResult{Status: ResolveNotFound}, nil
}

// Revoke revokes a managed link and marks it revoked.
func (s *Service) Revoke(ctx context.Context, link domain.InviteLink) error {
	chatID, err := s.chatID(link.Resource)
	if err != nil {
		return err
	}

	if _, err := s.links.RevokeChatInviteLink(ctx, chatID, link.InviteLink); err != nil {
		if markErr := s.store.MarkStatus(
			ctx, link.ID, domain.InviteFailed, nil, err.Error(),
		); markErr != nil {
			s.logger.Warn("failed to mark invite link failed",
				slog.Int64("invite_link_id", link.ID),
				slog.String("invite_link_hash", link.InviteLinkHash),
				slog.Any("error", markErr))
		}

		return err
	}

	return s.store.MarkStatus(ctx, link.ID, domain.InviteRevoked, nil, "")
}

// MarkSent records that an invite link has been delivered to a user.
func (s *Service) MarkSent(ctx context.Context, link domain.InviteLink) error {
	return s.store.MarkStatus(ctx, link.ID, domain.InviteSent, link.TGID, "")
}

// MarkUsed records that a personal invite successfully admitted its owner.
func (s *Service) MarkUsed(ctx context.Context, link domain.InviteLink) error {
	return s.store.MarkStatus(ctx, link.ID, domain.InviteUsed, nil, "")
}

func (s *Service) resolveMissingInviteLink(
	ctx context.Context,
	req ResolveRequest,
) (ResolveResult, error) {
	switch req.Mode {
	case domain.InviteSharedJoinRequest:
		link, ok, err := s.store.FindActiveShared(
			ctx, req.Resource, domain.InviteSharedJoinRequest)
		if err != nil {
			return ResolveResult{}, err
		}

		if ok {
			if usable, err := s.ensureLinkUsable(ctx, link); err != nil || !usable {
				return ResolveResult{Status: ResolveNotFound}, err
			}

			return ResolveResult{Status: ResolveSafeFallback, Link: &link}, nil
		}

		return ResolveResult{Status: ResolveSafeFallback}, nil
	case domain.InvitePersonalJoinRequest:
		link, ok, err := s.store.FindActivePersonal(
			ctx, req.TGID, req.Resource, domain.InvitePersonalJoinRequest)
		if err != nil || !ok {
			return ResolveResult{Status: ResolveNotFound}, err
		}

		if ok, err := s.ensureLinkUsable(ctx, link); err != nil || !ok {
			return ResolveResult{Status: ResolveNotFound}, err
		}

		return ResolveResult{Status: ResolveSafeFallback, Link: &link}, nil
	case domain.InviteDirect:
		link, ok, err := s.store.FindActivePersonal(
			ctx, req.TGID, req.Resource, domain.InviteDirect)
		if err != nil || !ok {
			return ResolveResult{Status: ResolveNotFound}, err
		}

		if ok, err := s.ensureLinkUsable(ctx, link); err != nil || !ok {
			return ResolveResult{Status: ResolveNotFound}, err
		}

		return ResolveResult{Status: ResolveSafeFallback, Link: &link}, nil
	default:
		return ResolveResult{Status: ResolveNotFound}, nil
	}
}

func (s *Service) resolveOwnedInvite(
	ctx context.Context,
	req ResolveRequest,
	link domain.InviteLink,
	accepted ResolveStatus,
) (ResolveResult, error) {
	if link.TGID != nil && *link.TGID == req.TGID {
		return ResolveResult{Status: accepted, Link: &link}, nil
	}

	attemptedBy := req.TGID
	if err := s.store.MarkStatus(
		ctx, link.ID, domain.InviteUsedByOther, &attemptedBy, "",
	); err != nil {
		return ResolveResult{}, err
	}

	link.AttemptedBy = &attemptedBy
	link.Status = domain.InviteUsedByOther

	return ResolveResult{Status: ResolveUsedByOther, Link: &link}, nil
}

func (s *Service) ensureLinkUsable(
	ctx context.Context,
	link domain.InviteLink,
) (bool, error) {
	if link.ExpiresAt == nil || s.now().Before(*link.ExpiresAt) {
		return true, nil
	}

	if err := s.store.MarkStatus(ctx, link.ID, domain.InviteExpired, nil, ""); err != nil {
		return false, err
	}

	return false, nil
}

func (s *Service) ensureShared(
	ctx context.Context,
	resource domain.Resource,
) (domain.InviteLink, error) {
	if link, ok, err := s.store.FindActiveShared(
		ctx, resource, domain.InviteSharedJoinRequest,
	); err != nil || ok {
		if ok {
			s.logReady(link)
		}

		return link, err
	}

	return s.create(ctx, createRequest{
		Resource:           resource,
		Mode:               domain.InviteSharedJoinRequest,
		CreatesJoinRequest: true,
	})
}

func (s *Service) ensurePersonal(
	ctx context.Context,
	req EnsureRequest,
) (domain.InviteLink, error) {
	if req.TGID == nil {
		return domain.InviteLink{}, errors.New("personal invite requires tg_id")
	}

	if link, ok, err := s.store.FindActivePersonal(
		ctx, *req.TGID, req.Resource, domain.InvitePersonalJoinRequest,
	); err != nil {
		return domain.InviteLink{}, err
	} else if ok {
		if link.ExpiresAt == nil || s.now().Before(*link.ExpiresAt) {
			s.logReady(link)

			return link, nil
		}

		if err := s.store.MarkStatus(
			ctx, link.ID, domain.InviteExpired, nil, "",
		); err != nil {
			return domain.InviteLink{}, err
		}
	}

	expiresAt := s.now().Add(s.cfg.TTL)

	return s.create(ctx, createRequest{
		TGID:               req.TGID,
		Resource:           req.Resource,
		Mode:               domain.InvitePersonalJoinRequest,
		CreatesJoinRequest: true,
		ExpiresAt:          &expiresAt,
	})
}

func (s *Service) ensureDirect(
	ctx context.Context,
	req EnsureRequest,
) (domain.InviteLink, error) {
	if req.TGID == nil {
		return domain.InviteLink{}, errors.New("direct invite requires tg_id")
	}

	if s.cfg.TTL > time.Hour {
		return domain.InviteLink{}, errors.New("direct invite ttl exceeds one hour")
	}

	if link, ok, err := s.store.FindActivePersonal(
		ctx, *req.TGID, req.Resource, domain.InviteDirect,
	); err != nil {
		return domain.InviteLink{}, err
	} else if ok {
		if link.ExpiresAt == nil || s.now().Before(*link.ExpiresAt) {
			s.logReady(link)

			return link, nil
		}

		if err := s.store.MarkStatus(
			ctx, link.ID, domain.InviteExpired, nil, "",
		); err != nil {
			return domain.InviteLink{}, err
		}
	}

	expiresAt := s.now().Add(s.cfg.TTL)

	return s.create(ctx, createRequest{
		TGID:               req.TGID,
		Resource:           req.Resource,
		Mode:               domain.InviteDirect,
		CreatesJoinRequest: false,
		MemberLimit:        1,
		ExpiresAt:          &expiresAt,
	})
}

type createRequest struct {
	TGID               *int64
	Resource           domain.Resource
	Mode               domain.InviteMode
	CreatesJoinRequest bool
	MemberLimit        int
	ExpiresAt          *time.Time
}

func (s *Service) create(
	ctx context.Context,
	req createRequest,
) (domain.InviteLink, error) {
	if s.links == nil {
		return domain.InviteLink{}, errors.New("invite link manager is nil")
	}

	if s.store == nil {
		return domain.InviteLink{}, errors.New("invite store is nil")
	}

	chatID, err := s.chatID(req.Resource)
	if err != nil {
		return domain.InviteLink{}, err
	}

	nonce, err := s.nonce()
	if err != nil {
		return domain.InviteLink{}, fmt.Errorf("generate invite nonce: %w", err)
	}

	name := telegramName(req.Mode, req.Resource, nonce)

	created, err := s.links.CreateChatInviteLink(ctx, CreateChatInviteLinkParams{
		ChatID:             chatID,
		Name:               name,
		ExpireAt:           req.ExpiresAt,
		MemberLimit:        req.MemberLimit,
		CreatesJoinRequest: req.CreatesJoinRequest,
	})
	if err != nil {
		return domain.InviteLink{}, err
	}

	link, err := s.store.SaveCreated(ctx, store.InviteLinkInput{
		TGID:               req.TGID,
		Resource:           req.Resource,
		Mode:               req.Mode,
		InviteLink:         created.InviteLink,
		InviteLinkHash:     HashInviteLink(created.InviteLink),
		TelegramName:       name,
		Nonce:              nonce,
		CreatesJoinRequest: req.CreatesJoinRequest,
		ExpiresAt:          req.ExpiresAt,
	})
	if err != nil {
		return domain.InviteLink{}, err
	}

	s.logReady(link)

	return link, nil
}

func (s *Service) chatID(resource domain.Resource) (int64, error) {
	switch resource {
	case domain.ResourceChat:
		return s.cfg.ClubChatID, nil
	case domain.ResourceChannel:
		return s.cfg.ClubChannelID, nil
	default:
		return 0, fmt.Errorf("unknown invite resource %q", resource)
	}
}

func (s *Service) logReady(link domain.InviteLink) {
	if s.logger == nil {
		return
	}

	s.logger.Debug("invite link ready",
		slog.String("resource", string(link.Resource)),
		slog.String("mode", string(link.Mode)),
		slog.String("invite_link_hash", link.InviteLinkHash),
		slog.String("telegram_name", link.TelegramName))
}

func telegramName(
	mode domain.InviteMode,
	resource domain.Resource,
	nonce string,
) string {
	modePart := inviteModeNamePart(mode)
	resourcePart := inviteResourceNamePart(resource)
	prefix := fmt.Sprintf("gk-%s-%s-", modePart, resourcePart)

	maxNonceLen := maxTelegramInviteNameLen - len(prefix)
	if len(nonce) > maxNonceLen {
		nonce = nonce[:maxNonceLen]
	}

	return prefix + nonce
}

func inviteModeNamePart(mode domain.InviteMode) string {
	switch mode {
	case domain.InviteSharedJoinRequest:
		return "sjr"
	case domain.InvitePersonalJoinRequest:
		return "pjr"
	case domain.InviteDirect:
		return "dir"
	default:
		return "unk"
	}
}

func inviteResourceNamePart(resource domain.Resource) string {
	switch resource {
	case domain.ResourceChat:
		return "chat"
	case domain.ResourceChannel:
		return "chan"
	default:
		return "res"
	}
}

// HashInviteLink returns the stable hash used for invite lookup and logs.
func HashInviteLink(link string) string {
	sum := sha256.Sum256([]byte(link))

	return hex.EncodeToString(sum[:])
}

func randomNonce() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}

	return hex.EncodeToString(b[:]), nil
}
