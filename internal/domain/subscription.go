package domain

import "time"

// Verdict is one source's answer about a user's status.
type Verdict string

const (
	VerdictActive   Verdict = "active"    // source positively confirmed the subscription
	VerdictInactive Verdict = "inactive"  // source positively said "no"
	VerdictUnknown  Verdict = "unknown"   // could not be determined (error/timeout)
	VerdictNoSignal Verdict = "no_signal" // source not applicable / no data (not a "no")
)

// EffectiveStatus is the result of merging the verdicts of all sources.
type EffectiveStatus string

const (
	StatusActive   EffectiveStatus = "active"
	StatusInactive EffectiveStatus = "inactive"
	StatusUnknown  EffectiveStatus = "unknown"
)

// SubscriptionStatus is the state of a subscription row in the database.
type SubscriptionStatus string

const (
	SubActive  SubscriptionStatus = "active"
	SubExpired SubscriptionStatus = "expired"
)

// Subscription is one subscription period of a user on one platform.
type Subscription struct {
	ID            int64
	TGID          int64
	Platform      Platform
	Status        SubscriptionStatus
	ExternalID    string
	PeriodID      string
	Tier          string
	StartedAt     time.Time
	ExpiresAt     *time.Time // nil if the date is unknown (observation)
	EndedAt       *time.Time
	LastSignal    string     // event|webhook|reconcile|on_demand|manual
	LastEventAt   *time.Time // created_at of the last webhook — for ordering
	LastCheckedAt *time.Time
}
