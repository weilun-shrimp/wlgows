package wlgows

import (
	"errors"
	"fmt"
	"testing"
)

/*
The sentinels are the package's error API: callers switch on them with
errors.Is. These pin the two properties that make that work — every sentinel is
a distinct value, and wrapping preserves identity at any depth.
*/

func allSentinels() map[string]error {
	return map[string]error{
		"ErrFrameByteLengthExceeded":         ErrFrameByteLengthExceeded,
		"ErrClientRequestHasSet":             ErrClientRequestHasSet,
		"ErrHttpMsgFormationInvalid":         ErrHttpMsgFormationInvalid,
		"ErrHttpMethodNotAllowed":            ErrHttpMethodNotAllowed,
		"ErrHttpProtocolOrVersionNotAllowed": ErrHttpProtocolOrVersionNotAllowed,
		"ErrHttpSecWebSocketKeyHeaderNotSet": ErrHttpSecWebSocketKeyHeaderNotSet,
		"ErrHttpConnectionHeaderNotUpgrade":  ErrHttpConnectionHeaderNotUpgrade,
		"ErrHttpUpgradeHeaderNotWebsocket":   ErrHttpUpgradeHeaderNotWebsocket,
		"ErrHttpRequestHasResponse":          ErrHttpRequestHasResponse,
	}
}

func TestSentinelsAreNonNilWithAMessage(t *testing.T) {
	for name, sentinel := range allSentinels() {
		if sentinel == nil {
			t.Errorf("%s is nil", name)
			continue
		}
		if sentinel.Error() == "" {
			t.Errorf("%s has an empty message", name)
		}
	}
}

// A duplicated variable would alias two names onto one value and make errors.Is
// answer true for the wrong one.
func TestSentinelsAreDistinct(t *testing.T) {
	for nameA, a := range allSentinels() {
		for nameB, b := range allSentinels() {
			if nameA == nameB {
				continue
			}
			if errors.Is(a, b) {
				t.Errorf("%s and %s are the same error value", nameA, nameB)
			}
		}
	}
}

// The whole point of the redesign: context in the message, identity preserved.
func TestSentinelSurvivesWrapping(t *testing.T) {
	for name, sentinel := range allSentinels() {
		t.Run(name, func(t *testing.T) {
			once := fmt.Errorf("layer one: %w", sentinel)
			twice := fmt.Errorf("layer two: %w", once)

			if !errors.Is(once, sentinel) {
				t.Error("errors.Is failed through one layer")
			}
			if !errors.Is(twice, sentinel) {
				t.Error("errors.Is failed through two layers")
			}
			// The context has to actually reach the message, or wrapping bought
			// nothing over returning the sentinel bare.
			if got := twice.Error(); got == sentinel.Error() {
				t.Errorf("wrapped message lost its context: %q", got)
			}
		})
	}
}

// Comparing with == instead of errors.Is is the trap this design introduces:
// what a caller receives is the wrapper, never the sentinel itself.
func TestWrappedErrorIsNotEqualToTheSentinel(t *testing.T) {
	wrapped := fmt.Errorf("context: %w", ErrHttpMethodNotAllowed)
	if wrapped == ErrHttpMethodNotAllowed {
		t.Error("a wrapped error must not compare equal with ==")
	}
	if !errors.Is(wrapped, ErrHttpMethodNotAllowed) {
		t.Error("errors.Is is the supported comparison and it failed")
	}
}
