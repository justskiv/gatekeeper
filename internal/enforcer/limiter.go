package enforcer

import (
	"context"
	"sync"
	"time"
)

const (
	messagePerChatInterval = time.Second
	globalRequestInterval  = 50 * time.Millisecond
	getMemberInterval      = 700 * time.Millisecond
)

type requestKind string

const (
	requestKindDefault   requestKind = "default"
	requestKindMessage   requestKind = "message"
	requestKindGetMember requestKind = "get_chat_member"
)

// RateLimiter throttles Telegram calls before a worker executes them.
type RateLimiter interface {
	Wait(ctx context.Context, kind requestKind, chatID int64) error
}

// NewRateLimiter returns the production Telegram limiter.
func NewRateLimiter() *rateLimiter {
	return &rateLimiter{
		perChat: make(map[int64]time.Time),
		now:     time.Now,
		sleep:   sleepContext,
	}
}

type rateLimiter struct {
	mu        sync.Mutex
	global    time.Time
	getMember time.Time
	perChat   map[int64]time.Time
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
}

func (l *rateLimiter) Wait(
	ctx context.Context,
	kind requestKind,
	chatID int64,
) error {
	for {
		wait := l.reserve(kind, chatID)
		if wait <= 0 {
			return nil
		}

		if err := l.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

func (l *rateLimiter) reserve(kind requestKind, chatID int64) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	readyAt := l.global

	if kind == requestKindGetMember && l.getMember.After(readyAt) {
		readyAt = l.getMember
	}

	if kind == requestKindMessage && l.perChat[chatID].After(readyAt) {
		readyAt = l.perChat[chatID]
	}

	if readyAt.After(now) {
		return readyAt.Sub(now)
	}

	next := now.Add(globalRequestInterval)
	l.global = next

	switch kind {
	case requestKindDefault:
	case requestKindGetMember:
		l.getMember = now.Add(getMemberInterval)
	case requestKindMessage:
		l.perChat[chatID] = now.Add(messagePerChatInterval)
	}

	return 0
}

type noopLimiter struct{}

func (noopLimiter) Wait(context.Context, requestKind, int64) error {
	return nil
}
