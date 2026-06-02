//nolint:wsl_v5 // Ops rendering adapters stay compact and mechanical.
package bot

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"

	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
)

const (
	alertListLimit = 20
	exportRowLimit = 1000
	replyChunkSize = 3500
)

// ChatRole is one configured chat shown by /chats.
type ChatRole struct {
	Role   string
	ChatID int64
}

func (h *UserCommands) handleStats(
	ctx context.Context,
	msg *models.Message,
) (Result, error) {
	if !h.isOwner(msg.From.ID) {
		return Result{Ignored: true}, nil
	}

	if h.deps.Ops == nil {
		return h.ownerReply(msg, messages.OpsUnavailable()), nil
	}

	stats, err := h.deps.Ops.Stats(ctx, time.Now())
	if err != nil {
		return Result{}, err
	}

	return h.ownerReply(msg, messages.OpsStats(statsMessageData(stats))), nil
}

func (h *UserCommands) handleAlerts(
	ctx context.Context,
	msg *models.Message,
) (Result, error) {
	if !h.isOwner(msg.From.ID) {
		return Result{Ignored: true}, nil
	}

	if h.deps.Ops == nil {
		return h.ownerReply(msg, messages.OpsUnavailable()), nil
	}

	alerts, err := h.deps.Ops.OpenAlerts(ctx, alertListLimit)
	if err != nil {
		return Result{}, err
	}

	return h.ownerReply(msg, messages.OpsAlerts(alertsMessageData(alerts))), nil
}

func (h *UserCommands) handleChats(msg *models.Message) (Result, error) {
	if !h.isOwner(msg.From.ID) {
		return Result{Ignored: true}, nil
	}

	if len(h.deps.ChatRoles) == 0 {
		return h.ownerReply(msg, messages.OpsUnavailable()), nil
	}

	return h.ownerReply(msg, messages.OpsChats(chatRolesMessageData(h.deps.ChatRoles))), nil
}

func (h *UserCommands) handleHelpAdmin(msg *models.Message) (Result, error) {
	if !h.isOwner(msg.From.ID) {
		return Result{Ignored: true}, nil
	}

	return h.ownerReply(msg, messages.AdminHelp()), nil
}

func (h *UserCommands) handleExport(
	ctx context.Context,
	msg *models.Message,
) (Result, error) {
	if !h.isOwner(msg.From.ID) {
		return Result{Ignored: true}, nil
	}

	if msg.Chat.Type != models.ChatTypePrivate {
		return Result{Ignored: true}, nil
	}

	if h.deps.Ops == nil {
		return h.ownerReply(msg, messages.OpsUnavailable()), nil
	}

	rows, err := h.deps.Ops.ExportRows(ctx, exportRowLimit)
	if err != nil {
		return Result{}, err
	}

	text, err := renderExportCSV(rows)
	if err != nil {
		return Result{}, err
	}

	parts := splitReply(text, replyChunkSize)
	replies := make([]Reply, 0, len(parts))
	for _, part := range parts {
		replies = append(replies, Reply{
			ChatID: msg.Chat.ID,
			TGID:   msg.From.ID,
			Text:   part,
			DM:     true,
		})
	}

	return Result{Replies: replies}, nil
}

func renderExportCSV(rows []store.ExportRow) (string, error) {
	var b bytes.Buffer
	writer := csv.NewWriter(&b)

	header := []string{
		"tg_id",
		"username",
		"first_name",
		"last_name",
		"language_code",
		"dm_state",
		"banned",
		"banned_reason",
		"active_subscriptions",
		"expires_at",
		"grant_chat",
		"grant_channel",
		"whitelisted",
		"last_seen_at",
	}
	if err := writer.Write(header); err != nil {
		return "", fmt.Errorf("write export header: %w", err)
	}

	for _, row := range rows {
		if err := writer.Write([]string{
			strconv.FormatInt(row.TGID, 10),
			row.Username,
			row.FirstName,
			row.LastName,
			row.LanguageCode,
			string(row.DMState),
			strconv.FormatBool(row.Banned),
			row.BannedReason,
			row.Subscriptions,
			row.ExpiresAt,
			row.GrantChat,
			row.GrantChannel,
			strconv.FormatBool(row.Whitelisted),
			formatOptionalTime(row.LastSeenAt),
		}); err != nil {
			return "", fmt.Errorf("write export row: %w", err)
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", fmt.Errorf("flush export csv: %w", err)
	}

	return b.String(), nil
}

func statsMessageData(stats store.OpsStats) messages.OpsStatsData {
	data := messages.OpsStatsData{
		PendingRevocations: stats.PendingRevocations,
		DueRevocations:     stats.DueRevocations,
		ReconcileLastRunAt: stats.ReconcileLastRunAt,
		OpenAlerts:         stats.OpenAlerts,
	}

	for _, count := range stats.ActiveSubscriptions {
		data.ActiveSubscriptions = append(data.ActiveSubscriptions, messages.NamedCount{
			Name:  count.Name,
			Count: count.Count,
		})
	}

	for _, count := range stats.Grants {
		data.Grants = append(data.Grants, messages.GrantCount{
			Resource: string(count.Resource),
			State:    string(count.State),
			Count:    count.Count,
		})
	}

	for key, value := range stats.Health {
		data.Health = append(data.Health, messages.NamedValue{
			Name:  key,
			Value: value,
		})
	}

	for _, count := range stats.Outbox {
		data.Outbox = append(data.Outbox, messages.OutboxCount{
			Status: string(count.Status),
			Count:  count.Count,
		})
	}

	return data
}

func alertsMessageData(alerts []store.OpsAlert) []messages.OpsAlertData {
	out := make([]messages.OpsAlertData, 0, len(alerts))
	for _, alert := range alerts {
		out = append(out, messages.OpsAlertData{
			ID:        alert.ID,
			Severity:  alert.Severity,
			Kind:      alert.Kind,
			Title:     alert.Title,
			CreatedAt: alert.CreatedAt,
		})
	}

	return out
}

func chatRolesMessageData(roles []ChatRole) []messages.ChatRoleData {
	out := make([]messages.ChatRoleData, 0, len(roles))
	for _, role := range roles {
		out = append(out, messages.ChatRoleData{
			Role:   role.Role,
			ChatID: role.ChatID,
		})
	}

	return out
}

func splitReply(text string, limit int) []string {
	if limit <= 0 || len(text) <= limit {
		return []string{text}
	}

	var parts []string
	for len(text) > limit {
		cut := strings.LastIndexByte(text[:limit], '\n')
		if cut <= 0 {
			cut = safeReplyCut(text, limit)
		}

		parts = append(parts, text[:cut])
		text = strings.TrimPrefix(text[cut:], "\n")
	}

	if text != "" {
		parts = append(parts, text)
	}

	return parts
}

func safeReplyCut(text string, limit int) int {
	if limit >= len(text) {
		return len(text)
	}

	for cut := limit; cut > 0; cut-- {
		if utf8.RuneStart(text[cut]) {
			return cut
		}
	}

	_, size := utf8.DecodeRuneInString(text)
	if size > 0 {
		return size
	}

	return limit
}

func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.UTC().Format(time.RFC3339)
}
