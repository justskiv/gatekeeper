package operatorlog

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justskiv/gatekeeper/internal/domain"
	"github.com/justskiv/gatekeeper/internal/messages"
	"github.com/justskiv/gatekeeper/internal/store"
)

const eventLogChatID int64 = -1006666666666

type fakeOutbox struct {
	inputs  []store.AccessActionInput
	enqErr  error
	inserts int
}

func (o *fakeOutbox) Enqueue(
	_ context.Context,
	input store.AccessActionInput,
) (domain.AccessAction, bool, error) {
	if o.enqErr != nil {
		return domain.AccessAction{}, false, o.enqErr
	}

	o.inputs = append(o.inputs, input)
	o.inserts++

	return domain.AccessAction{ID: int64(o.inserts)}, true, nil
}

func okRender(domain.OperatorEvent) (string, error) { return "rendered", nil }

func newWriter(render Renderer) *Writer {
	return New(eventLogChatID, render, nil)
}

func sampleEvent() domain.OperatorEvent {
	return domain.OperatorEvent{
		Kind:   domain.OpAccessGranted,
		TGID:   12345,
		Marker: "active_since:2026-06-01T00:00:00Z",
	}
}

func TestWriterEmitTargetsEventLogChatWithHTML(t *testing.T) {
	outbox := &fakeOutbox{}
	w := newWriter(okRender)

	require.NoError(t, w.Emit(context.Background(), outbox, sampleEvent()))
	require.Len(t, outbox.inputs, 1)

	in := outbox.inputs[0]
	assert.Equal(t, domain.ActionSendDM, in.Type)
	require.NotNil(t, in.TGID)
	assert.Equal(t, int64(12345), *in.TGID)

	var payload struct {
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode"`
		ChatID    int64  `json:"chat_id"`
	}
	require.NoError(t, json.Unmarshal(in.PayloadJSON, &payload))
	assert.Equal(t, "rendered", payload.Text)
	assert.Equal(t, messages.ParseModeHTML, payload.ParseMode)
	assert.Equal(t, eventLogChatID, payload.ChatID,
		"event delivery must target EVENT_LOG_CHAT_ID, never fall back")
}

func TestWriterEmitRenderErrorSkipsWithoutFailing(t *testing.T) {
	outbox := &fakeOutbox{}
	w := newWriter(func(domain.OperatorEvent) (string, error) {
		return "", errors.New("template missing")
	})

	// A feed-build error must be swallowed: the domain change is not rolled
	// back, so Emit returns nil and nothing is enqueued.
	require.NoError(t, w.Emit(context.Background(), outbox, sampleEvent()))
	assert.Empty(t, outbox.inputs, "render failure must skip the event")
}

func TestWriterEmitPersistenceErrorPropagates(t *testing.T) {
	outbox := &fakeOutbox{enqErr: errors.New("db down")}
	w := newWriter(okRender)

	// A persistence failure is a database failure: it must propagate so the
	// caller's transaction fails like any other durable write.
	require.Error(t, w.Emit(context.Background(), outbox, sampleEvent()))
}

func TestWriterEmitIdempotencyKeyIsStableAndMarkerScoped(t *testing.T) {
	outbox := &fakeOutbox{}
	w := newWriter(okRender)
	ev := sampleEvent()

	require.NoError(t, w.Emit(context.Background(), outbox, ev))
	require.NoError(t, w.Emit(context.Background(), outbox, ev))
	require.Len(t, outbox.inputs, 2)
	assert.Equal(t, outbox.inputs[0].IdempotencyKey, outbox.inputs[1].IdempotencyKey,
		"same kind+subject+marker must yield the same key so retries dedupe")

	other := ev
	other.Marker = "active_since:2026-07-01T00:00:00Z"
	require.NoError(t, w.Emit(context.Background(), outbox, other))
	assert.NotEqual(t, outbox.inputs[0].IdempotencyKey, outbox.inputs[2].IdempotencyKey,
		"a different episode marker must yield a different key")

	differentKind := ev
	differentKind.Kind = domain.OpAccessKept
	require.NoError(t, w.Emit(context.Background(), outbox, differentKind))
	assert.NotEqual(t, outbox.inputs[0].IdempotencyKey, outbox.inputs[3].IdempotencyKey,
		"a different kind must yield a different key")
}

func TestWriterNilAndNilOutboxAreNoops(t *testing.T) {
	var nilWriter *Writer
	require.NoError(t, nilWriter.Emit(context.Background(), &fakeOutbox{}, sampleEvent()))

	w := newWriter(okRender)
	require.NoError(t, w.Emit(context.Background(), nil, sampleEvent()))
}
