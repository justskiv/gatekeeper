package domain

import "time"

// DMState tells whether the bot can send a direct message to a user.
type DMState string

const (
	DMUnknown DMState = "unknown" // not tried yet / user never started the bot
	DMOpen    DMState = "open"    // messaged successfully — allowed
	DMBlocked DMState = "blocked" // user blocked the bot
)

// User is a Telegram user known to the system. Identity is the tg_id.
type User struct {
	TGID         int64
	Username     string
	FirstName    string
	LastName     string
	LanguageCode string
	IsBot        bool
	DMState      DMState
	Banned       bool // hard-ban by the owner
	BannedReason string
	Notes        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastSeenAt   time.Time
}
