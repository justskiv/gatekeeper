// Package domain holds the value types shared across the whole
// application. It depends on nothing but the standard library — the
// dependency direction always points towards domain, and the package
// contains no behavioural interfaces.
package domain

import "time"

// Platform identifies a subscription source.
type Platform string

const (
	PlatformBoosty  Platform = "boosty"
	PlatformTribute Platform = "tribute"
	PlatformManual  Platform = "manual"
)

// EventKind is the normalized kind of a subscription event.
type EventKind string

const (
	// EventActivated means the subscription appeared or is active.
	EventActivated EventKind = "activated"
	// EventDeactivated means the subscription is gone or expired.
	EventDeactivated EventKind = "deactivated"
	// EventCancelledSubscription means the provider sent a cancellation notice.
	EventCancelledSubscription EventKind = "cancelled_subscription"
)

// SubscriptionEvent is a normalized subscription event from any source.
// The core of the system does not know where the event came from.
type SubscriptionEvent struct {
	Platform       Platform
	Kind           EventKind
	TGUserID       int64
	TGUsername     string     // optional
	TGFirstName    string     // optional Telegram profile cache
	TGLastName     string     // optional Telegram profile cache
	TGLanguageCode string     // optional Telegram profile cache
	TGIsBot        bool       // source events for bots are ignored before engine
	Tier           string     // optional (subscription_name for Tribute)
	ExpiresAt      *time.Time // known only for Tribute mode B and for manual
	ExternalID     string     // tribute subscription_id, etc.
	PeriodID       string     // tribute period_id
	OccurredAt     time.Time
	Raw            []byte // raw payload for auditing
}
