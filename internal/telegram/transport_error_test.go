package telegram

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	botapi "github.com/go-telegram/bot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deadlineError(method string) error {
	return fmt.Errorf("error do request for method %s, %w", method, &url.Error{
		Op:  "Post",
		URL: "https://api.telegram.org/botSECRET/" + method,
		Err: context.DeadlineExceeded,
	})
}

func TestNormalizeErrorClassifiesTimeout(t *testing.T) {
	err := NormalizeError("sendMessage", deadlineError("sendMessage"))

	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, ErrorCategoryTimeout, apiErr.Category)

	assert.True(t, IsTimeout(err), "IsTimeout must recognize it")
	assert.True(t, IsTransient(err), "a timeout is worth retrying")
	assert.False(t, IsRateLimited(err))
	assert.False(t, IsForbidden(err))
	assert.False(t, IsDMBlocked(err))
	assert.False(t, IsPermanentRights(err))
}

func TestNormalizeErrorClassifiesCanceled(t *testing.T) {
	err := NormalizeError("sendMessage",
		fmt.Errorf("do request: %w", context.Canceled))

	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, ErrorCategoryCanceled, apiErr.Category)
	assert.False(t, IsTimeout(err), "cancellation is not a timeout")
}

// TestNormalizeErrorRateLimitWinsOverTimeout pins the classification order: a
// 429 whose chain happens to carry a deadline must keep its retry_after.
func TestNormalizeErrorRateLimitWinsOverTimeout(t *testing.T) {
	rate := &botapi.TooManyRequestsError{
		Message:    "too many requests",
		RetryAfter: 17,
	}
	err := NormalizeError("sendMessage",
		fmt.Errorf("%w: %w", rate, context.DeadlineExceeded))

	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, ErrorCategoryRateLimited, apiErr.Category,
		"a rate limit must not be reclassified as a timeout")

	retryAfter, ok := RetryAfter(err)
	require.True(t, ok, "retry_after must survive")
	assert.Equal(t, 17*time.Second, retryAfter)
}

// TestNormalizeErrorClassifiesNetTimeout covers "i/o timeout", which is a
// timeout without carrying context.DeadlineExceeded.
func TestNormalizeErrorClassifiesNetTimeout(t *testing.T) {
	netErr := &net.OpError{
		Op:  "read",
		Err: &timeoutOnlyError{},
	}
	err := NormalizeError("getChatMember", fmt.Errorf("do request: %w", netErr))

	assert.True(t, IsTimeout(err), "net.Error timeouts must classify as timeout")
}

type timeoutOnlyError struct{}

func (*timeoutOnlyError) Error() string { return "i/o timeout" }
func (*timeoutOnlyError) Timeout() bool { return true }

// TestRawRequestPreservesChainAndRedactsToken is the regression for the change
// that made transport errors classifiable: the chain must survive %w while the
// token must not survive into the message.
func TestRawRequestPreservesChainAndRedactsToken(t *testing.T) {
	const token = "123456:SUPER-SECRET-TOKEN"

	srv := httptest.NewServer(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) {
			time.Sleep(300 * time.Millisecond)
		}))
	defer srv.Close()

	client, err := NewClient(token,
		WithServerURL(srv.URL),
		WithHTTPClient(&http.Client{Timeout: 30 * time.Millisecond}))
	require.NoError(t, err, "new client")

	err = client.rawRequest(context.Background(), "banChatMember",
		map[string]any{"chat_id": -1, "user_id": 1}, nil)
	require.Error(t, err, "the request must time out")

	require.ErrorIs(t, err, context.DeadlineExceeded,
		"the error chain must survive wrapping, otherwise a timeout is "+
			"indistinguishable from a shutdown")
	assert.NotContains(t, err.Error(), token,
		"the bot token must never reach the logs")
	assert.Contains(t, err.Error(), "***", "the token must be redacted in place")
}

// TestRawRequestRedactsTokenFromNestedTransportError covers what a
// struct-level scrub cannot reach. The HTTP client is replaceable, and a
// RoundTripper may render the request URL into its own error text; that text
// then flows into APIError.Error(), into access_actions.last_error and into the
// logs. Redaction must therefore hold for the whole rendered message, and the
// error must stay classifiable while it does.
func TestRawRequestRedactsTokenFromNestedTransportError(t *testing.T) {
	const token = "123456:SUPER-SECRET-TOKEN"

	client, err := NewClient(token,
		WithServerURL("https://api.telegram.test"),
		WithHTTPClient(&http.Client{Transport: roundTripFunc(
			func(req *http.Request) (*http.Response, error) {
				// A transport that quotes the request URL verbatim — exactly
				// what a proxy-aware or instrumented RoundTripper does.
				return nil, fmt.Errorf("upstream refused %s: %w",
					req.URL.String(), context.DeadlineExceeded)
			})}))
	require.NoError(t, err, "new client")

	err = client.rawRequest(context.Background(), "banChatMember",
		map[string]any{"chat_id": -1, "user_id": 1}, nil)
	require.Error(t, err, "the transport must fail")

	assert.NotContains(t, err.Error(), token,
		"the token must not survive anywhere in the rendered error")
	require.ErrorIs(t, err, context.DeadlineExceeded,
		"redaction must not cost the error chain")

	normalized := NormalizeError("banChatMember", err)
	assert.NotContains(t, normalized.Error(), token,
		"the normalized error is what reaches last_error and the logs")
	assert.True(t, IsTimeout(normalized),
		"a redacted timeout is still a timeout")
}

// TestNormalizedTimeoutFromRawRequestIsClassified ties the two halves together:
// a real transport timeout, once normalized, lands in the timeout category.
func TestNormalizedTimeoutFromRawRequestIsClassified(t *testing.T) {
	const token = "123456:SUPER-SECRET-TOKEN"

	srv := httptest.NewServer(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) {
			time.Sleep(300 * time.Millisecond)
		}))
	defer srv.Close()

	client, err := NewClient(token,
		WithServerURL(srv.URL),
		WithHTTPClient(&http.Client{Timeout: 30 * time.Millisecond}))
	require.NoError(t, err, "new client")

	rawErr := client.rawRequest(context.Background(), "banChatMember",
		map[string]any{"chat_id": -1, "user_id": 1}, nil)
	require.Error(t, rawErr)

	normalized := NormalizeError("banChatMember", rawErr)
	assert.True(t, IsTimeout(normalized))
	assert.NotContains(t, normalized.Error(), token)
}
