package domain

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"time"
)

// Resource is a bot-managed club resource.
type Resource string

const (
	ResourceChat    Resource = "chat"
	ResourceChannel Resource = "channel"
)

// InviteMode is how the bot hands out a link to a managed resource.
type InviteMode string

const (
	InviteSharedJoinRequest   InviteMode = "shared_join_request"
	InvitePersonalJoinRequest InviteMode = "personal_join_request"
	InviteDirect              InviteMode = "direct"
)

// InviteStatus is the lifecycle of an invite link created by the bot.
type InviteStatus string

const (
	InviteCreated     InviteStatus = "created"
	InviteSent        InviteStatus = "sent"
	InviteUsed        InviteStatus = "used"
	InviteUsedByOther InviteStatus = "used_by_other"
	InviteRevoked     InviteStatus = "revoked"
	InviteExpired     InviteStatus = "expired"
	InviteFailed      InviteStatus = "failed"
)

// ActionType is a durable Telegram side effect recorded in access_actions.
type ActionType string

const (
	ActionEnsureInvite ActionType = "ensure_invite"
	ActionSendInvite   ActionType = "send_invite"
	ActionApproveJoin  ActionType = "approve_join"
	ActionDeclineJoin  ActionType = "decline_join"
	ActionSoftKick     ActionType = "soft_kick"
	ActionHardBan      ActionType = "hard_ban"
	ActionUnban        ActionType = "unban"
	ActionSendDM       ActionType = "send_dm"
	ActionEditMessage  ActionType = "edit_message"
	ActionVerifyMember ActionType = "verify_member"
	ActionRevokeInvite ActionType = "revoke_invite"
)

// ActionStatus is the access_actions execution state machine.
type ActionStatus string

const (
	ActionQueued  ActionStatus = "queued"
	ActionRunning ActionStatus = "running"
	ActionDone    ActionStatus = "done"
	ActionFailed  ActionStatus = "failed"
	ActionDead    ActionStatus = "dead"
)

// AccessAction is one durable Telegram action owned by the Enforcer.
type AccessAction struct {
	ID             int64
	Type           ActionType
	TGID           *int64
	Resource       *Resource
	IdempotencyKey string
	PayloadJSON    []byte
	Status         ActionStatus
	RunAfter       time.Time
	Attempts       int
	MaxAttempts    int
	LockedUntil    *time.Time
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// AccessActionKey builds the canonical idempotency key for outbox rows.
// The marker is hashed so callers can include verbose payload-derived
// context without leaking message text or invite URLs into the key.
func AccessActionKey(
	action ActionType,
	tgID *int64,
	resource *Resource,
	marker string,
) string {
	tgPart := "-"
	if tgID != nil {
		tgPart = strconv.FormatInt(*tgID, 10)
	}

	resourcePart := "-"
	if resource != nil {
		resourcePart = string(*resource)
	}

	markerHash := sha256.Sum256([]byte(marker))

	return fmt.Sprintf("%s:%s:%s:%x",
		action, tgPart, resourcePart, markerHash[:12])
}

// GrantState is the state of access to a single resource.
type GrantState string

const (
	GrantPending GrantState = "pending" // link issued, no join yet
	GrantJoined  GrantState = "joined"  // user is in the resource
	GrantLeft    GrantState = "left"    // user left on their own
	GrantRevoked GrantState = "revoked" // removed by the bot
)

// AccessGrant is a user's access to a single resource.
type AccessGrant struct {
	ID            int64
	TGID          int64
	Resource      Resource
	State         GrantState
	AdmittedBy    string // bot|external
	JoinedAt      *time.Time
	RevokedAt     *time.Time
	RevokedReason string
	UpdatedAt     time.Time
}

// InviteLink is a link created by the bot to grant access.
// For shared_join_request TGID is nil: there is one link per resource.
type InviteLink struct {
	ID                 int64
	TGID               *int64
	Resource           Resource
	Mode               InviteMode
	InviteLink         string
	InviteLinkHash     string
	TelegramName       string
	Nonce              string
	Status             InviteStatus
	CreatesJoinRequest bool
	ExpiresAt          *time.Time
	SentAt             *time.Time
	UsedAt             *time.Time
	RevokedAt          *time.Time
	AttemptedBy        *int64
	LastError          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// PendingRevocation is a scheduled access revocation (grace period).
type PendingRevocation struct {
	TGID        int64
	Reason      string
	ScheduledAt time.Time
	Notified    bool
	CreatedAt   time.Time
}

// AccessDecision is an explainable access-check result, used by /whois
// and the logs.
type AccessDecision struct {
	TGID    int64
	Status  EffectiveStatus
	Allowed bool
	Reasons []AccessReason // one per source plus whitelist/ban
}

// AccessReason explains why a source produced its verdict.
type AccessReason struct {
	Source  Platform // boosty|tribute|manual; or the service values whitelist|ban
	Verdict Verdict
	Detail  string     // human-readable: "member of the Boosty group", "expires_at passed"
	Until   *time.Time // if known
}

// SourceVerdict is one source observation before aggregation.
type SourceVerdict struct {
	Source  Platform
	Verdict Verdict
	Detail  string
	Until   *time.Time
}
