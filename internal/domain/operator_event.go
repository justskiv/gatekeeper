package domain

import "time"

// OperatorEventKind identifies an entry in the operator event log — the
// owner-facing feed of access and membership lifecycle events.
type OperatorEventKind string

const (
	// OpAccessGranted reports an effective eligibility transition to active.
	OpAccessGranted OperatorEventKind = "access_granted"
	// OpAccessLossScheduled reports a grace-period revocation being scheduled
	// while access is still present.
	OpAccessLossScheduled OperatorEventKind = "access_loss_scheduled"
	// OpAccessLost reports an actual revocation of effective or resource access.
	OpAccessLost OperatorEventKind = "access_lost"
	// OpAccessKept reports a pending revocation cancelled by active access.
	OpAccessKept OperatorEventKind = "access_kept"

	// OpClubChatJoined / OpClubChatLeft report observed club chat membership.
	OpClubChatJoined OperatorEventKind = "club_chat_joined"
	OpClubChatLeft   OperatorEventKind = "club_chat_left"
	// OpClubChannelSubscribed / OpClubChannelUnsubscribed report observed club
	// channel membership.
	OpClubChannelSubscribed   OperatorEventKind = "club_channel_subscribed"
	OpClubChannelUnsubscribed OperatorEventKind = "club_channel_unsubscribed"

	// OpSourceSubscriptionActivated / OpSourceSubscriptionExpired report raw
	// per-platform subscription source changes.
	OpSourceSubscriptionActivated OperatorEventKind = "source_subscription_activated"
	OpSourceSubscriptionExpired   OperatorEventKind = "source_subscription_expired"

	// OpManualGrant / OpManualRevoke / OpManualBan / OpManualUnban report
	// confirmed owner commands.
	OpManualGrant  OperatorEventKind = "manual_grant"
	OpManualRevoke OperatorEventKind = "manual_revoke"
	OpManualBan    OperatorEventKind = "manual_ban"
	OpManualUnban  OperatorEventKind = "manual_unban"

	// OpBannedJoinAttempt reports a hard-banned user entering a managed
	// resource with a hard-ban removal enqueued (removal is asynchronous).
	OpBannedJoinAttempt OperatorEventKind = "banned_join_attempt"
)

// AdmissionMethod is how a user entered or was admitted to a managed resource.
type AdmissionMethod string

const (
	AdmissionBotLink  AdmissionMethod = "bot_link" // bot admission evidence
	AdmissionExternal AdmissionMethod = "external" // no bot evidence
	AdmissionAdmin    AdmissionMethod = "admin"    // owner command
	AdmissionProvider AdmissionMethod = "provider" // source event
	AdmissionJob      AdmissionMethod = "job"      // reconcile / revocation
)

// OperatorActor is the agent that caused an operator event.
type OperatorActor string

const (
	OperatorActorSystem   OperatorActor = "system"
	OperatorActorUser     OperatorActor = "user"
	OperatorActorAdmin    OperatorActor = "admin"
	OperatorActorProvider OperatorActor = "provider"
	OperatorActorJob      OperatorActor = "job"
)

// LossMode describes how access loss came about, when known.
type LossMode string

const (
	LossGrace     LossMode = "grace"
	LossImmediate LossMode = "immediate"
	LossManual    LossMode = "manual"
)

// OperatorEvent is the typed, safe-context model for one operator event-log
// entry. It is rendered from existing local data only: no full invite URLs,
// raw payloads, secrets or internal chat IDs ever enter this struct.
type OperatorEvent struct {
	Kind OperatorEventKind

	// Subject identity.
	TGID      int64
	Username  string // cached @username without the @, optional
	UserLabel string // safe display name (first + last), optional

	// Resource scope. Resource is set for membership/source-of-a-resource
	// events; Resources lists every resource affected by an access event.
	Resource  *Resource
	Resources []Resource

	Actor  OperatorActor
	Method AdmissionMethod // admission method, when applicable

	// ActorLabel is a safe label for an external actor taken from
	// ChatMemberUpdated.from; empty when Telegram did not expose one.
	ActorLabel string

	// ActiveSources lists every source currently granting access
	// (boosty/tribute/manual/whitelist), so the feed shows *why* the user
	// has access. Empty means no active source is known.
	ActiveSources []Platform

	// Fallback marks an admission that proceeded on a stored fresh active
	// subscription because the live status was unknown.
	Fallback bool

	// Invite context — safe fields only.
	InviteMode     InviteMode
	InviteLinkHash string

	// Source-event context.
	Platform      Platform
	Tier          string
	ProviderEvent string
	ExpiresAt     *time.Time

	// Loss / manual context.
	LossMode     LossMode
	Reason       string // safe revocation reason (system/job revoke path)
	ManualReason string // safe owner-provided reason
	HardBan      bool   // hard-ban overrides an otherwise active source
	ScheduledAt  *time.Time

	// EventTime is the source-stable timestamp of the originating event.
	EventTime time.Time

	// Marker is the source-stable idempotency token for the event. It MUST
	// come from the originating update (Telegram/provider event time,
	// join-request date, confirmed action id, active-episode anchor) and
	// MUST NOT be derived from process wall-clock or render time.
	Marker string
}

// PlatformWhitelist is the service platform label for whitelist-based access.
const PlatformWhitelist Platform = "whitelist"

// ActiveSourceReasons lists the platforms currently granting access from a
// decision's reasons, deduplicated. Whitelist is the storage form of manual
// access, so it is reported to the operator feed as `manual`, never as a
// separate source the user did not request. Service verdicts such as ban are
// excluded. It lets every emit point describe *all* active reasons
// consistently.
func ActiveSourceReasons(decision AccessDecision) []Platform {
	var (
		sources []Platform
		seen    = make(map[Platform]struct{})
	)

	for _, reason := range decision.Reasons {
		if reason.Verdict != VerdictActive {
			continue
		}

		platform, ok := feedSourcePlatform(reason.Source)
		if !ok {
			continue
		}

		if _, dup := seen[platform]; dup {
			continue
		}

		seen[platform] = struct{}{}
		sources = append(sources, platform)
	}

	return sources
}

// feedSourcePlatform maps a decision source to the platform shown in the
// operator feed, normalizing whitelist to manual. Non-source verdicts (ban)
// return false.
func feedSourcePlatform(source Platform) (Platform, bool) {
	switch source {
	case PlatformBoosty, PlatformTribute, PlatformManual:
		return source, true
	case PlatformWhitelist:
		return PlatformManual, true
	default:
		return "", false
	}
}

// AccessEpisodeMarker derives the per-episode idempotency anchor shared by
// the engine transition and the admission fallback so an active access
// episode logs access_granted at most once. It is the earliest start time
// among the currently active subscriptions; whitelist-only or unknown access
// fall back to a stable label. now bounds expiry so an expired subscription
// does not anchor a live episode.
func AccessEpisodeMarker(
	subs []Subscription,
	whitelisted bool,
	now time.Time,
) string {
	var earliest *time.Time

	for i := range subs {
		sub := subs[i]
		if sub.Status != SubActive {
			continue
		}

		if sub.ExpiresAt != nil && !now.Before(*sub.ExpiresAt) {
			continue
		}

		if earliest == nil || sub.StartedAt.Before(*earliest) {
			started := sub.StartedAt
			earliest = &started
		}
	}

	if earliest != nil {
		return "active_since:" + earliest.UTC().Format(time.RFC3339Nano)
	}

	if whitelisted {
		return "active_since:whitelist"
	}

	return "active_since:unknown"
}
