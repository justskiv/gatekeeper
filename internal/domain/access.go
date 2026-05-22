package domain

import "time"

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
