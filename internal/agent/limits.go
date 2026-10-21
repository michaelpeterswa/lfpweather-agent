package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
)

// BusyError reports that the Anthropic API rate-limited a model call. The
// session's history is unchanged, so the same question can simply be asked
// again once RetryAfter has passed.
type BusyError struct {
	RetryAfter time.Duration // zero when the API did not say
	Err        error
}

func (e *BusyError) Error() string { return "rate limited: " + e.Err.Error() }
func (e *BusyError) Unwrap() error { return e.Err }

// UserMessage is the text shown to the person asking.
func (e *BusyError) UserMessage() string {
	if s := int(e.RetryAfter.Round(time.Second) / time.Second); s > 0 {
		return fmt.Sprintf("The assistant is busy right now. Try again in %d seconds.", s)
	}
	return "The assistant is busy right now. Try again in a few seconds."
}

// classifyModelError turns a model-call failure into a BusyError for a 429,
// and reports whether any other failure is worth one immediate retry
// (overloaded, server errors, and transport errors).
//
// Model calls are made with SDK retries off. The SDK honours retry-after
// verbatim with no cap, so a rate-limited follow-up used to sit silent for the
// whole request deadline before failing with "context deadline exceeded".
func classifyModelError(err error) (busy *BusyError, retryable bool) {
	var apierr *anthropic.Error
	if !errors.As(err, &apierr) {
		// Transport errors; a context error means the deadline is spent.
		return nil, !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	}
	switch {
	case apierr.StatusCode == 429:
		return &BusyError{RetryAfter: retryAfter(apierr), Err: err}, false
	case apierr.StatusCode >= 500: // includes 529 overloaded
		return nil, true
	}
	return nil, false
}

func retryAfter(apierr *anthropic.Error) time.Duration {
	if apierr.Response == nil {
		return 0
	}
	if s, err := strconv.ParseFloat(apierr.Response.Header.Get("Retry-After"), 64); err == nil && s > 0 {
		return time.Duration(s * float64(time.Second))
	}
	return 0
}

// maxToolResultBytes caps one tool result before it enters the conversation.
// Every result stays in the session's history and is re-sent with each later
// question, so one oversized result taxes the whole conversation. 24 KB is
// about 6k tokens: several times what a normal query_weather result needs.
const maxToolResultBytes = 24 << 10

// truncateToolResult shortens an oversized tool result on a UTF-8 boundary and
// says so, so the model can narrow its query instead of trusting a partial
// series as complete.
func truncateToolResult(s string) string {
	if len(s) <= maxToolResultBytes {
		return s
	}
	cut := maxToolResultBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf(
		"\n\n[result truncated: %d of %d bytes shown. Query a shorter range or a coarser bucket for the full series.]",
		cut, len(s))
}
