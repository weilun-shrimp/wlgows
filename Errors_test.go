package wlgows

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
)

/*
StandardClosePayloadFor is the table RFC 6455 7.4.1 fixes, so what it pins is
the split between the three status codes and the errors that get none. The nil
rows carry the whole point: an error reaching Listen is not evidence that a
close frame can be sent, or that the connection should be closed.
*/
func TestStandardClosePayloadFor(t *testing.T) {
	tests := []struct {
		name string
		errs []error
		want *ClosePayload
	}{
		{"a frame broke the framing rules", []error{
			ErrReservedBitsSet,
			ErrFrameNotMasked,
			ErrFrameMasked,
			ErrControlFrameFragmented,
			ErrControlFramePayloadTooLong,
			ErrContinuationFrameWithoutMsg,
			ErrDataFrameDuringMsg,
			ErrInvalidCloseStatusCode,
			ErrClosePayloadTooShort,
		}, &ClosePayload{StatusCode: CloseProtocolError}},

		{"the payload did not match its opcode", []error{
			ErrInvalidUTF8,
		}, &ClosePayload{StatusCode: CloseInvalidFramePayloadData}},

		{"a limit the caller set was passed", []error{
			ErrFrameByteLengthExceeded,
			ErrMsgFrameCountExceeded,
		}, &ClosePayload{StatusCode: CloseMessageTooBig}},

		// The connection is already gone, so there is nothing to send it. 7.4.1
		// names this 1006 and forbids 1006 on the wire.
		{"the connection failed", []error{
			io.EOF,
			io.ErrUnexpectedEOF,
			os.ErrDeadlineExceeded,
			net.ErrClosed,
		}, nil},

		// Returned before Listen reads anything. ErrListenerIsListening leaves a
		// healthy connection in another goroutine's hands.
		{"the caller misused the Listener", []error{
			ErrListenerConnIsNil,
			ErrListenerIsListening,
		}, nil},

		// A custom net.Conn or a wrapping layer can return anything at all
		// through GetNextFrame. Guessing a status code would blame the peer for
		// something nothing here can attribute to them.
		{"an error this package has never seen", []error{
			errors.New("XXXXX"),
			fmt.Errorf("read failed: %w", errors.New("YYYYY")),
		}, nil},

		{"nothing failed", []error{nil}, nil},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			for _, err := range testCase.errs {
				got := StandardClosePayloadFor(err)
				switch {
				case got == nil && testCase.want == nil:
				case got == nil || testCase.want == nil:
					t.Errorf("StandardClosePayloadFor(%v) = %+v, want %+v", err, got, testCase.want)
				case *got != *testCase.want:
					t.Errorf("StandardClosePayloadFor(%v) = %+v, want %+v", err, *got, *testCase.want)
				}
			}
		})
	}
}

/*
Nothing in the package returns a bare sentinel — every one arrives wrapped with
%w, sometimes twice. StandardClosePayloadFor is useless if it only matches the bare
value.
*/
func TestStandardClosePayloadForWrapped(t *testing.T) {
	wrapped := fmt.Errorf("listen: %w", fmt.Errorf("frame 3: %w", ErrInvalidUTF8))

	got := StandardClosePayloadFor(wrapped)
	if got == nil || got.StatusCode != CloseInvalidFramePayloadData {
		t.Fatalf("StandardClosePayloadFor(%v) = %+v, want %d", wrapped, got, CloseInvalidFramePayloadData)
	}
}

// The caller owns what the peer is told, so nothing is filled in for them.
func TestStandardClosePayloadForLeavesReasonEmpty(t *testing.T) {
	got := StandardClosePayloadFor(ErrReservedBitsSet)
	if got.Reason != "" {
		t.Errorf("Reason = %q, want empty", got.Reason)
	}
}
