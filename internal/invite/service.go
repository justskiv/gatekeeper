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
	"github.com/justskiv/gatekeeper/internal/telegram"
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

	created, err := s.links.CreateChatInviteLink(ctx, telegram.CreateChatInviteLinkParams{
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
		InviteLinkHash:     hashInviteLink(created.InviteLink),
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

func hashInviteLink(link string) string {
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
