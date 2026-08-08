package wlgows

import (
	"errors"
	"testing"
)

/*
nextFrameByteLimit is what the next frame may declare. One number serves both
classes, because the opcode is not known until the frame has been read: data
frames get whatever is left of MaxMsgPayloadByteLen, control frames get their
RFC 6455 5.5 allowance of 125 whatever is left.
*/
func TestListenerNextFrameByteLimit(t *testing.T) {
	tests := []struct {
		name                 string
		maxMsgPayloadByteLen uint64
		currentDataAccLength uint64
		want                 uint64
	}{
		{"no limit", 0, 0, 0},
		{"nothing spent", 1000, 0, 1000},
		{"partly spent", 1000, 30, 970},
		// Below the control allowance the floor takes over, or a ping arriving
		// late in a large message would be refused for being 125 bytes.
		{"remainder under the floor", 1000, 900, ControlFramePayloadMaxByteLength},
		{"exactly spent", 1000, 1000, ControlFramePayloadMaxByteLength},
		// Pausing mid message and lowering MaxMsgPayloadByteLen leaves
		// currentDataAccLength above it. An unguarded uint64 subtraction would
		// wrap to ~1.8e19 and hand back a limit larger than the one just set.
		{"overspent", 1000, 5000, ControlFramePayloadMaxByteLength},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			l := &Listener{currentDataAccLength: testCase.currentDataAccLength}
			l.config.MaxMsgPayloadByteLen = testCase.maxMsgPayloadByteLen

			if got := l.nextFrameByteLimit(); got != testCase.want {
				t.Errorf("nextFrameByteLimit() = %d, want %d", got, testCase.want)
			}
		})
	}
}

/*
validateFrame refuses a frame that is malformed on its own terms. Every rule is
a MUST in RFC 6455, and the caller answers all of them with close code 1002.

PeerIsClient is false throughout except where masking is the subject, so the
unmasked frames these build are the ones a server peer would send.
*/
func TestListenerValidateFrame(t *testing.T) {
	// 5.2: the reserved bits belong to negotiated extensions, and none is.
	t.Run("RSV bits", func(t *testing.T) {
		for name, frame := range map[string]*Frame{
			"RSV1": {Opcode: OpcodePing, FIN: true, RSV1: true},
			"RSV2": {Opcode: OpcodePing, FIN: true, RSV2: true},
			"RSV3": {Opcode: OpcodePing, FIN: true, RSV3: true},
		} {
			t.Run(name, func(t *testing.T) {
				if err := (&Listener{}).validateFrame(frame); !errors.Is(err, ErrReservedBitsSet) {
					t.Errorf("err = %v, want ErrReservedBitsSet", err)
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
				l := &Listener{}
				l.config.PeerIsClient = testCase.peerIsClient

				err := l.validateFrame(&Frame{Opcode: OpcodePing, FIN: true, Mask: testCase.mask})
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
				"126 bytes",
				&Frame{Opcode: OpcodePing, FIN: true, PayloadData: make([]byte, 126)},
				ErrControlFramePayloadTooLong,
			},
			{
				"125 bytes",
				&Frame{Opcode: OpcodePing, FIN: true, PayloadData: make([]byte, 125)},
				nil,
			},
		}
		for _, testCase := range tests {
			t.Run(testCase.name, func(t *testing.T) {
				if err := (&Listener{}).validateFrame(testCase.frame); !errors.Is(err, testCase.want) {
					t.Errorf("err = %v, want %v", err, testCase.want)
				}
			})
		}
	})

	// A reserved opcode carries no rule to check against, so it passes through
	// to Unknown untouched — including the fragmentation and size rules that
	// would apply if we claimed to know which class it was.
	t.Run("reserved opcodes", func(t *testing.T) {
		for _, opcode := range []byte{0x3, 0x7, 0xB, 0xF} {
			frame := &Frame{Opcode: opcode, PayloadData: make([]byte, 200)}
			if err := (&Listener{}).validateFrame(frame); err != nil {
				t.Errorf("opcode %#x: err = %v, want nil", opcode, err)
			}
		}
	})

	// 5.4: a data opcode opens a message and every frame after it carries
	// opcode 0, so the frame has to agree with whether one is open.
	t.Run("fragmentation", func(t *testing.T) {
		tests := []struct {
			name              string
			currentDataFrames Frames
			frame             *Frame
			want              error
		}{
			{"opens a message", nil, &Frame{Opcode: OpcodeText}, nil},
			{
				"continues one",
				Frames{{Opcode: OpcodeText}},
				&Frame{Opcode: OpcodeContinuation},
				nil,
			},
			{
				"continuation with none open",
				nil,
				&Frame{Opcode: OpcodeContinuation},
				ErrContinuationFrameWithoutMsg,
			},
			{
				"data opcode during one",
				Frames{{Opcode: OpcodeText}},
				&Frame{Opcode: OpcodeBinary},
				ErrDataFrameDuringMsg,
			},
			{
				"same data opcode during one",
				Frames{{Opcode: OpcodeText}},
				&Frame{Opcode: OpcodeText},
				ErrDataFrameDuringMsg,
			},
		}
		for _, testCase := range tests {
			t.Run(testCase.name, func(t *testing.T) {
				l := &Listener{currentDataFrames: testCase.currentDataFrames}

				if err := l.validateFrame(testCase.frame); !errors.Is(err, testCase.want) {
					t.Errorf("err = %v, want %v", err, testCase.want)
				}
			})
		}
	})
}
