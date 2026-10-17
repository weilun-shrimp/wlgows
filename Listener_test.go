package wlgows

import (
	"errors"
	"sync"
	"testing"
)

func TestNewListener(t *testing.T) {
	conn, _ := hookConn()
	agreed := &PermessageDeflateConfig{}

	l := NewListener(conn, NewListenerConfig{
		PeerIsClient:                  true,
		PermessageDeflateAgreedConfig: agreed,
	})

	if l.conn != ListenerConn(conn) {
		t.Error("conn was not set")
	}
	if !l.peerIsClient {
		t.Error("peerIsClient was not set")
	}
	if l.permessageDeflateAgreedConfig != agreed {
		t.Error("permessageDeflateAgreedConfig was not set")
	}
	// The zero Listener panics on the first Lock, which is why this exists.
	if l.configLocker == nil || l.listenLocker == nil {
		t.Error("NewListener must populate both lockers")
	}
}

func TestListenerIsCurrentMsgDeflated(t *testing.T) {
	t.Run("is whether the message in flight is deflated", func(t *testing.T) {
		l := &Listener{}
		if l.IsCurrentMsgDeflated() {
			t.Error("IsCurrentMsgDeflated = true with none open, want false")
		}
		l.currentDataFramesDeflated = true
		if !l.IsCurrentMsgDeflated() {
			t.Error("IsCurrentMsgDeflated = false for a deflated message, want true")
		}
	})
}

/*
validateFrameHeader refuses a frame by its header, before its payload is read.
Every rule is a MUST in RFC 6455, answered with close code 1002; the limits are
the Listener's own, answered with 1009.

peerIsClient is false throughout except where masking is the subject, so the
unmasked frames these build are the ones a server peer would send.
*/
func TestListenerValidateFrameHeader(t *testing.T) {
	// 5.2: the reserved bits belong to negotiated extensions. RFC 7692 6, 6.1:
	// permessage-deflate takes RSV1, on the first frame of a data message only.
	t.Run("RSV bits", func(t *testing.T) {
		tests := []struct {
			name        string
			agreed      bool
			messageOpen bool
			frame       *Frame
			want        error
		}{
			{
				name:  "RSV1 on text, not agreed",
				frame: &Frame{Opcode: OpcodeText, FIN: true, RSV1: true},
				want:  ErrReservedBitsSet,
			},
			{
				name:  "RSV1 on ping, not agreed",
				frame: &Frame{Opcode: OpcodePing, FIN: true, RSV1: true},
				want:  ErrReservedBitsSet,
			},
			{
				name:  "RSV2, not agreed",
				frame: &Frame{Opcode: OpcodePing, FIN: true, RSV2: true},
				want:  ErrReservedBitsSet,
			},
			{
				name:  "RSV3, not agreed",
				frame: &Frame{Opcode: OpcodePing, FIN: true, RSV3: true},
				want:  ErrReservedBitsSet,
			},
			{
				name:   "RSV1 on a first text frame, agreed",
				agreed: true,
				frame:  &Frame{Opcode: OpcodeText, RSV1: true},
				want:   nil,
			},
			{
				name:   "RSV1 on a first binary frame, agreed",
				agreed: true,
				frame:  &Frame{Opcode: OpcodeBinary, FIN: true, RSV1: true},
				want:   nil,
			},
			{
				name:        "RSV1 on a continuation, agreed",
				agreed:      true,
				messageOpen: true,
				frame:       &Frame{Opcode: OpcodeContinuation, FIN: true, RSV1: true},
				want:        ErrReservedBitsSet,
			},
			{
				name:   "RSV1 on ping, agreed",
				agreed: true,
				frame:  &Frame{Opcode: OpcodePing, FIN: true, RSV1: true},
				want:   ErrReservedBitsSet,
			},
			{
				name:   "RSV1 on a reserved opcode, agreed",
				agreed: true,
				frame:  &Frame{Opcode: 0x3, FIN: true, RSV1: true},
				want:   ErrReservedBitsSet,
			},
			{
				name:   "RSV2 on text, agreed",
				agreed: true,
				frame:  &Frame{Opcode: OpcodeText, FIN: true, RSV2: true},
				want:   ErrReservedBitsSet,
			},
			{
				name:   "RSV3 on text, agreed",
				agreed: true,
				frame:  &Frame{Opcode: OpcodeText, FIN: true, RSV3: true},
				want:   ErrReservedBitsSet,
			},
			// Passes the bits as a first frame would, then 5.4 refuses it.
			{
				name:        "RSV1 on text while a message is open, agreed",
				agreed:      true,
				messageOpen: true,
				frame:       &Frame{Opcode: OpcodeText, FIN: true, RSV1: true},
				want:        ErrDataFrameDuringMsg,
			},
		}
		for _, testCase := range tests {
			t.Run(testCase.name, func(t *testing.T) {
				l := &Listener{configLocker: &sync.Mutex{}}
				if testCase.agreed {
					l.permessageDeflateAgreedConfig = &PermessageDeflateConfig{}
				}
				if testCase.messageOpen {
					l.currentDataFrameCount = 1
					l.currentDataFrameOpcode = OpcodeText
				}

				if err := l.validateFrameHeader(testCase.frame); !errors.Is(err, testCase.want) {
					t.Errorf("err = %v, want %v", err, testCase.want)
				}
			})
		}
	})

	// 5.1: a client masks every frame it sends, a server masks none.
	t.Run("masking", func(t *testing.T) {
		tests := []struct {
			name         string
			peerIsClient bool
			mask         bool
			want         error
		}{
			{"client masked", true, true, nil},
			{"client unmasked", true, false, ErrFrameNotMasked},
			{"server unmasked", false, false, nil},
			{"server masked", false, true, ErrFrameMasked},
		}
		for _, testCase := range tests {
			t.Run(testCase.name, func(t *testing.T) {
				l := &Listener{configLocker: &sync.Mutex{}}
				l.peerIsClient = testCase.peerIsClient

				err := l.validateFrameHeader(&Frame{Opcode: OpcodePing, FIN: true, Mask: testCase.mask})
				if !errors.Is(err, testCase.want) {
					t.Errorf("err = %v, want %v", err, testCase.want)
				}
			})
		}
	})

	// 5.5: control frames are never fragmented and never exceed 125 bytes.
	t.Run("control frames", func(t *testing.T) {
		tests := []struct {
			name  string
			frame *Frame
			want  error
		}{
			{"ok", &Frame{Opcode: OpcodePing, FIN: true}, nil},
			{"fragmented", &Frame{Opcode: OpcodePing}, ErrControlFrameFragmented},
			{
				"125 bytes",
				&Frame{Opcode: OpcodePing, FIN: true, PayloadLength: 125},
				nil,
			},
			{
				"126 bytes",
				&Frame{Opcode: OpcodePing, FIN: true, PayloadLength: 126, ExtendedPayloadLength: 126},
				ErrControlFramePayloadTooLong,
			},
		}
		for _, testCase := range tests {
			t.Run(testCase.name, func(t *testing.T) {
				if err := (&Listener{configLocker: &sync.Mutex{}}).validateFrameHeader(testCase.frame); !errors.Is(err, testCase.want) {
					t.Errorf("err = %v, want %v", err, testCase.want)
				}
			})
		}
	})

	// 5.2: a reserved opcode goes to the Unknown hook, so only MaxFrameSize
	// applies — not the fragment order, the message budget or the frame count,
	// since it never joins a message. Its header here is 2 bytes.
	t.Run("reserved opcodes", func(t *testing.T) {
		tests := []struct {
			name         string
			maxFrameSize uint64
			frame        *Frame
			want         error
		}{
			{
				name:  "passes with no limit",
				frame: &Frame{Opcode: 0x3, PayloadLength: 100},
				want:  nil,
			},
			// MaxDataFramesSize does not bound it, so with no MaxFrameSize it
			// passes whatever it declares.
			{
				name:  "passes any size when MaxFrameSize is 0",
				frame: &Frame{Opcode: 0x3, PayloadLength: 127, ExtendedPayloadLength: 1 << 40},
				want:  nil,
			},
			{
				name:         "passes exactly at MaxFrameSize",
				maxFrameSize: 102,
				frame:        &Frame{Opcode: 0xB, FIN: true, PayloadLength: 100},
				want:         nil,
			},
			{
				name:         "refused one byte over MaxFrameSize",
				maxFrameSize: 101,
				frame:        &Frame{Opcode: 0xF, FIN: true, PayloadLength: 100},
				want:         ErrFrameByteLengthExceeded,
			},
		}
		for _, testCase := range tests {
			t.Run(testCase.name, func(t *testing.T) {
				// A message open with its budget spent: neither may touch a
				// reserved opcode.
				listener := &Listener{configLocker: &sync.Mutex{}, currentDataFrameCount: 1, currentDataFramesSize: 10}
				listener.config.MaxFrameSize = testCase.maxFrameSize
				listener.config.MaxDataFramesSize = 10
				listener.config.MaxDataFrameCount = 1

				if err := listener.validateFrameHeader(testCase.frame); !errors.Is(err, testCase.want) {
					t.Errorf("err = %v, want %v", err, testCase.want)
				}
				if listener.currentDataFrameCount != 1 || listener.currentDataFramesSize != 10 {
					t.Error("a reserved opcode must not touch the message being assembled")
				}
			})
		}
	})

	// 5.4: a data opcode opens a message and every frame after it carries
	// opcode 0, so the frame has to agree with whether one is open.
	t.Run("fragmentation", func(t *testing.T) {
		// The count is what says a message is open, since a Data hook retains no
		// frames to read that off — the last case is that Listener.
		tests := []struct {
			name                  string
			currentDataFrameCount uint64
			frame                 *Frame
			want                  error
		}{
			{"opens a message", 0, &Frame{Opcode: OpcodeText}, nil},
			{"continues one", 1, &Frame{Opcode: OpcodeContinuation}, nil},
			{
				"continuation with none open",
				0,
				&Frame{Opcode: OpcodeContinuation},
				ErrContinuationFrameWithoutMsg,
			},
			{"data opcode during one", 1, &Frame{Opcode: OpcodeBinary}, ErrDataFrameDuringMsg},
			{"same data opcode during one", 1, &Frame{Opcode: OpcodeText}, ErrDataFrameDuringMsg},
			{"open with nothing retained", 3, &Frame{Opcode: OpcodeText}, ErrDataFrameDuringMsg},
		}
		for _, testCase := range tests {
			t.Run(testCase.name, func(t *testing.T) {
				l := &Listener{configLocker: &sync.Mutex{}, currentDataFrameCount: testCase.currentDataFrameCount}

				if err := l.validateFrameHeader(testCase.frame); !errors.Is(err, testCase.want) {
					t.Errorf("err = %v, want %v", err, testCase.want)
				}
			})
		}
	})

	/*
		10.4: the limits are judged from the header. Every frame here is
		unmasked with a 7 bit length, so its header is 2 bytes. A message over a
		limit can never complete, so its state is reset.
	*/
	t.Run("limits", func(t *testing.T) {
		tests := []struct {
			name                  string
			maxFrameSize          uint64
			maxDataFramesSize     uint64
			maxDataFrameCount     uint64
			currentDataFrameCount uint64
			currentDataFramesSize uint64
			frame                 *Frame
			want                  error
		}{
			{
				name:  "no limits",
				frame: &Frame{Opcode: OpcodeText, PayloadLength: 125},
				want:  nil,
			},
			{
				name:         "a frame exactly at MaxFrameSize, header included",
				maxFrameSize: 102,
				frame:        &Frame{Opcode: OpcodeText, PayloadLength: 100},
				want:         nil,
			},
			{
				name:         "a frame one byte over MaxFrameSize because of its header",
				maxFrameSize: 101,
				frame:        &Frame{Opcode: OpcodeText, PayloadLength: 100},
				want:         ErrFrameByteLengthExceeded,
			},
			{
				name:                  "a frame exactly filling MaxDataFramesSize",
				maxDataFramesSize:     100,
				currentDataFrameCount: 1,
				currentDataFramesSize: 50,
				frame:                 &Frame{Opcode: OpcodeContinuation, PayloadLength: 48},
				want:                  nil,
			},
			{
				name:                  "a frame one byte over MaxDataFramesSize",
				maxDataFramesSize:     100,
				currentDataFrameCount: 1,
				currentDataFramesSize: 50,
				frame:                 &Frame{Opcode: OpcodeContinuation, PayloadLength: 49},
				want:                  ErrFrameByteLengthExceeded,
			},
			{
				name:                  "an empty frame still spends its header",
				maxDataFramesSize:     100,
				currentDataFrameCount: 1,
				currentDataFramesSize: 99,
				frame:                 &Frame{Opcode: OpcodeContinuation},
				want:                  ErrFrameByteLengthExceeded,
			},
			// Pausing mid message and lowering MaxDataFramesSize leaves the
			// message over it. An unguarded uint64 subtraction would wrap.
			{
				name:                  "a message already over a lowered MaxDataFramesSize",
				maxDataFramesSize:     100,
				currentDataFrameCount: 1,
				currentDataFramesSize: 500,
				frame:                 &Frame{Opcode: OpcodeContinuation},
				want:                  ErrFrameByteLengthExceeded,
			},
			{
				name:                  "the last frame MaxDataFrameCount allows",
				maxDataFrameCount:     3,
				currentDataFrameCount: 2,
				frame:                 &Frame{Opcode: OpcodeContinuation},
				want:                  nil,
			},
			{
				name:                  "one frame over MaxDataFrameCount",
				maxDataFrameCount:     3,
				currentDataFrameCount: 3,
				frame:                 &Frame{Opcode: OpcodeContinuation},
				want:                  ErrDataFrameCountExceeded,
			},
			// A control frame has its own fixed cap of 125, not these limits.
			{
				name:                  "a ping under every limit",
				maxFrameSize:          10,
				maxDataFramesSize:     10,
				maxDataFrameCount:     1,
				currentDataFrameCount: 1,
				currentDataFramesSize: 10,
				frame:                 &Frame{Opcode: OpcodePing, FIN: true, PayloadLength: 100},
				want:                  nil,
			},
		}
		for _, testCase := range tests {
			t.Run(testCase.name, func(t *testing.T) {
				listener := &Listener{
					configLocker:          &sync.Mutex{},
					currentDataFrames:     DataFrames{},
					currentDataFrameCount: testCase.currentDataFrameCount,
					currentDataFramesSize: testCase.currentDataFramesSize,
				}
				listener.config.MaxFrameSize = testCase.maxFrameSize
				listener.config.MaxDataFramesSize = testCase.maxDataFramesSize
				listener.config.MaxDataFrameCount = testCase.maxDataFrameCount

				err := listener.validateFrameHeader(testCase.frame)
				if !errors.Is(err, testCase.want) {
					t.Errorf("err = %v, want %v", err, testCase.want)
				}
				// A refused message is dropped; an allowed frame leaves the state alone.
				refused := testCase.want != nil
				if refused && (listener.currentDataFrameCount != 0 || listener.currentDataFramesSize != 0) {
					t.Errorf("currentDataFrameCount=%d currentDataFramesSize=%d after a refusal, want 0 and 0",
						listener.currentDataFrameCount, listener.currentDataFramesSize)
				}
				if !refused && (listener.currentDataFrameCount != testCase.currentDataFrameCount ||
					listener.currentDataFramesSize != testCase.currentDataFramesSize) {
					t.Error("an allowed frame must not change the totals; routeFrame spends them")
				}
			})
		}
	})
}
