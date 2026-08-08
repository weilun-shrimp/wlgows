package wlgows

import (
	"errors"
	"sync"
	"testing"
)

/*
routeFrame is the only thing that calls a hook, and the only thing that grows
currentDataFrames. So these check both: which hook a frame reaches, and what the
assembly state looks like afterwards.
*/
func TestListenerRouteFrame(t *testing.T) {
	// Control frames go to their own hook and never join the message being
	// assembled — RFC 6455 5.4 lets one arrive between two continuation frames.
	t.Run("control frames and unknown frame", func(t *testing.T) {
		tests := []struct {
			name   string
			opcode byte
			hook   func(*ListenerConfig, *[]*Frame)
		}{
			{"Ping", OpcodePing, func(c *ListenerConfig, got *[]*Frame) {
				c.Ping = func(f *Frame) { *got = append(*got, f) }
			}},
			{"Pong", OpcodePong, func(c *ListenerConfig, got *[]*Frame) {
				c.Pong = func(f *Frame) { *got = append(*got, f) }
			}},
			{"Close", OpcodeClose, func(c *ListenerConfig, got *[]*Frame) {
				c.Close = func(f *Frame) { *got = append(*got, f) }
			}},
			{"Unknown", 0xB, func(c *ListenerConfig, got *[]*Frame) {
				c.Unknown = func(f *Frame) { *got = append(*got, f) }
			}},
		}
		for _, testCase := range tests {
			t.Run(testCase.name, func(t *testing.T) {
				var got []*Frame
				l := &Listener{configLocker: &sync.Mutex{}}
				testCase.hook(&l.config, &got)

				// A message half assembled, to prove the control frame leaves it be.
				l.currentDataFrames = Frames{{Opcode: OpcodeText, PayloadData: []byte("he")}}
				l.currentDataAccLength = 2

				frame := &Frame{Opcode: testCase.opcode, FIN: true}
				if err := l.routeFrame(frame); err != nil {
					t.Fatalf("routeFrame: %v", err)
				}
				if len(got) != 1 || got[0] != frame {
					t.Errorf("the hook received %d frame(s), want the one routed", len(got))
				}
				if len(l.currentDataFrames) != 1 || l.currentDataAccLength != 2 {
					t.Error("a control frame must not touch currentDataFrames")
				}
			})
		}
	})

	// A nil hook drops the frame, which is what makes a partly configured
	// Listener readable rather than a panic.
	t.Run("nil hooks", func(t *testing.T) {
		l := &Listener{configLocker: &sync.Mutex{}}
		for _, opcode := range []byte{OpcodePing, OpcodePong, OpcodeClose, 0xB} {
			if err := l.routeFrame(&Frame{Opcode: opcode, FIN: true}); err != nil {
				t.Errorf("opcode %#x: %v", opcode, err)
			}
		}
		if err := l.routeFrame(&Frame{Opcode: OpcodeText, FIN: true}); err != nil {
			t.Errorf("OpcodeText: %v", err)
		}
	})

	t.Run("Text across fragments", func(t *testing.T) {
		var got Frames
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Text = func(frames Frames) { got = frames }

		for _, frame := range []*Frame{
			{Opcode: OpcodeText, PayloadData: []byte("he")},
			{Opcode: OpcodeContinuation, PayloadData: []byte("llo ")},
			{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("中文")},
		} {
			if err := l.routeFrame(frame); err != nil {
				t.Fatalf("routeFrame: %v", err)
			}
		}

		if got.String() != "hello 中文" {
			t.Errorf("Text got %q, want %q", got.String(), "hello 中文")
		}
		// Reset on FIN, or the next message starts with this one's frames and
		// this one's spent budget.
		if len(l.currentDataFrames) != 0 || l.currentDataAccLength != 0 {
			t.Errorf("currentDataFrames=%d currentDataAccLength=%d after FIN, want 0 and 0",
				len(l.currentDataFrames), l.currentDataAccLength)
		}
	})

	// The type comes from the first frame, so a binary message reaches Binary
	// even though its continuations carry opcode 0.
	t.Run("Binary", func(t *testing.T) {
		var got Frames
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Binary = func(frames Frames) { got = frames }

		l.routeFrame(&Frame{Opcode: OpcodeBinary, PayloadData: []byte{0x00}})
		l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte{0xFF}})

		if len(got) != 2 || got.Bytes()[0] != 0x00 || got.Bytes()[1] != 0xFF {
			t.Errorf("Binary got % x", got.Bytes())
		}
	})

	// Not fragmented on purpose: nothing else may reach a data hook, since a
	// FIN frame with no continuations is a whole message on its own.
	t.Run("Text unfragmented", func(t *testing.T) {
		called := 0
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Text = func(Frames) { called++ }

		l.routeFrame(&Frame{Opcode: OpcodeText, FIN: true, PayloadData: []byte("hi")})

		if called != 1 {
			t.Errorf("Text called %d times, want 1", called)
		}
	})

	// The read limit grants the larger of the control allowance and what the
	// message had left, so a data frame can still arrive over budget.
	t.Run("MaxMsgPayloadByteLen", func(t *testing.T) {
		called := 0
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.MaxMsgPayloadByteLen = 10
		l.config.Text = func(Frames) { called++ }

		if err := l.routeFrame(&Frame{Opcode: OpcodeText, PayloadData: make([]byte, 6)}); err != nil {
			t.Fatalf("6 bytes of a 10 byte budget: %v", err)
		}
		err := l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true, PayloadData: make([]byte, 5)})
		if !errors.Is(err, ErrFrameByteLengthExceeded) {
			t.Errorf("err = %v, want ErrFrameByteLengthExceeded", err)
		}
		if called != 0 {
			t.Error("a message over budget must not reach its hook")
		}
		// The message can never complete, so holding its frames serves nothing.
		if len(l.currentDataFrames) != 0 || l.currentDataAccLength != 0 {
			t.Errorf("currentDataFrames=%d currentDataAccLength=%d, want 0 and 0",
				len(l.currentDataFrames), l.currentDataAccLength)
		}
	})

	/*
		resetCurrentDataFrames allocates a fresh slice rather than reslicing to
		[:0]. Reusing the array would let the next message overwrite the frames
		the previous hook is still holding.
	*/
	t.Run("hook Frames survive the next message", func(t *testing.T) {
		var first Frames
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Text = func(frames Frames) {
			if first == nil {
				first = frames
			}
		}

		l.routeFrame(&Frame{Opcode: OpcodeText, FIN: true, PayloadData: []byte("first")})
		l.routeFrame(&Frame{Opcode: OpcodeText, FIN: true, PayloadData: []byte("second")})

		if first.String() != "first" {
			t.Errorf("the first message became %q after the second arrived", first.String())
		}
	})
}

/*
The Data hook takes each data frame where the message would otherwise be
assembled. So these check both sides of that swap: what reaches the hook, and
what the Listener stops doing — no frames retained, no message handed to Text or
Binary, no UTF-8 check.
*/
func TestListenerRouteFrameDataHook(t *testing.T) {
	t.Run("every frame of the message, in order", func(t *testing.T) {
		var got Frames
		textCalled, binaryCalled := 0, 0
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Data = func(f *Frame) { got = append(got, f) }
		l.config.Text = func(Frames) { textCalled++ }
		l.config.Binary = func(Frames) { binaryCalled++ }

		l.routeFrame(&Frame{Opcode: OpcodeText, PayloadData: []byte("he")})
		l.routeFrame(&Frame{Opcode: OpcodeContinuation, PayloadData: []byte("llo ")})
		l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("中文")})

		if got.String() != "hello 中文" {
			t.Errorf("Data received %q, want %q", got.String(), "hello 中文")
		}
		if textCalled != 0 || binaryCalled != 0 {
			t.Errorf("Text called %d times and Binary %d, want 0 and 0", textCalled, binaryCalled)
		}
	})

	// currentDataFrames never grows — that is the whole point, a message larger
	// than memory cannot be held in it. The other two fields still move: they
	// are the budgets, and the count is what says a message is open.
	t.Run("currentDataFrames stays empty, the totals do not", func(t *testing.T) {
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Data = func(*Frame) {}

		l.routeFrame(&Frame{Opcode: OpcodeBinary, PayloadData: []byte{0x00}})
		l.routeFrame(&Frame{Opcode: OpcodeContinuation, PayloadData: []byte{0x01}})

		if len(l.currentDataFrames) != 0 {
			t.Errorf("currentDataFrames holds %d frames, want 0 — nothing may be appended",
				len(l.currentDataFrames))
		}
		if l.currentDataFrameCount != 2 || l.currentDataAccLength != 2 {
			t.Errorf("currentDataFrameCount=%d currentDataAccLength=%d, want 2 and 2",
				l.currentDataFrameCount, l.currentDataAccLength)
		}
	})

	t.Run("FIN reopens the state", func(t *testing.T) {
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Data = func(*Frame) {}

		l.routeFrame(&Frame{Opcode: OpcodeText, PayloadData: []byte("hi")})
		l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("!")})

		if l.currentDataFrameCount != 0 || l.currentDataAccLength != 0 {
			t.Errorf("currentDataFrameCount=%d currentDataAccLength=%d after FIN, want 0 and 0",
				l.currentDataFrameCount, l.currentDataAccLength)
		}
		// Left behind, the next message would open as a continuation of this one
		// and validateFrame would refuse its opening frame.
		if l.currentDataFrameOpcode != OpcodeContinuation {
			t.Errorf("currentDataFrameOpcode = %#x after FIN, want 0", l.currentDataFrameOpcode)
		}
	})

	/*
		A continuation frame does not carry the message's type, so without this
		the hook cannot tell a text message from a binary one — and only text
		owes RFC 6455 5.6 a UTF-8 check. It has to answer for the FIN frame too,
		which is where a caller would run that check.
	*/
	t.Run("GetCurrentMsgOpcode through the message", func(t *testing.T) {
		var got []byte
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Data = func(*Frame) { got = append(got, l.GetCurrentMsgOpcode()) }

		l.routeFrame(&Frame{Opcode: OpcodeBinary, PayloadData: []byte{0x00}})
		l.routeFrame(&Frame{Opcode: OpcodeContinuation, PayloadData: []byte{0x01}})
		l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte{0x02}})

		for i, opcode := range got {
			if opcode != OpcodeBinary {
				t.Errorf("frame %d saw opcode %#x, want OpcodeBinary", i, opcode)
			}
		}
		if len(got) != 3 {
			t.Errorf("Data called %d times, want 3", len(got))
		}
		// Between messages there is no type to report.
		if l.GetCurrentMsgOpcode() != OpcodeContinuation {
			t.Errorf("GetCurrentMsgOpcode = %#x after FIN, want 0", l.GetCurrentMsgOpcode())
		}
	})

	// 5.6 can only be judged on the joined bytes, and nothing here joins them.
	// Checking is the caller's, and so is answering 1007.
	t.Run("no UTF-8 check", func(t *testing.T) {
		called := 0
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Data = func(*Frame) { called++ }

		err := l.routeFrame(&Frame{Opcode: OpcodeText, FIN: true, PayloadData: []byte{0xFF, 0xFE}})

		if err != nil {
			t.Errorf("err = %v, want nil", err)
		}
		if called != 1 {
			t.Errorf("Data called %d times, want 1", called)
		}
	})

	// Control frames have their own hooks and never joined the message anyway.
	t.Run("control frames are untouched", func(t *testing.T) {
		dataCalled, pingCalled := 0, 0
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Data = func(*Frame) { dataCalled++ }
		l.config.Ping = func(*Frame) { pingCalled++ }

		l.routeFrame(&Frame{Opcode: OpcodePing, FIN: true})

		if pingCalled != 1 || dataCalled != 0 {
			t.Errorf("Ping called %d times and Data %d, want 1 and 0", pingCalled, dataCalled)
		}
	})

	// Both messages below are 6 bytes in 3 frames against a budget of 4 bytes or
	// 2 frames, so the third frame busts it and is refused with the same error
	// Text would have got — the hook sees the first two and never the third.
	t.Run("MaxMsgPayloadByteLen and MaxMsgFrameCount still refuse the message", func(t *testing.T) {
		tests := []struct {
			name   string
			config func(*ListenerConfig)
			want   error
		}{
			{"MaxMsgPayloadByteLen", func(c *ListenerConfig) { c.MaxMsgPayloadByteLen = 4 }, ErrFrameByteLengthExceeded},
			{"MaxMsgFrameCount", func(c *ListenerConfig) { c.MaxMsgFrameCount = 2 }, ErrMsgFrameCountExceeded},
		}
		for _, testCase := range tests {
			t.Run(testCase.name, func(t *testing.T) {
				called := 0
				l := &Listener{configLocker: &sync.Mutex{}}
				l.config.Data = func(*Frame) { called++ }
				testCase.config(&l.config)

				l.routeFrame(&Frame{Opcode: OpcodeText, PayloadData: []byte("ab")})
				l.routeFrame(&Frame{Opcode: OpcodeContinuation, PayloadData: []byte("cd")})
				err := l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("ef")})

				if !errors.Is(err, testCase.want) {
					t.Errorf("err = %v, want %v", err, testCase.want)
				}
				if called != 2 {
					t.Errorf("Data called %d times, want 2 — the frame over budget must not reach it", called)
				}
				// It can never complete, so the state reopens for the next one.
				if l.currentDataFrameCount != 0 || l.currentDataAccLength != 0 {
					t.Error("the assembly state should have been reset")
				}
			})
		}
	})

	// Dropped the same way as with Text, so the frames the hook sees are exactly
	// the ones the message is made of.
	t.Run("empty continuation is dropped", func(t *testing.T) {
		called := 0
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Data = func(*Frame) { called++ }

		l.routeFrame(&Frame{Opcode: OpcodeText, PayloadData: []byte("hi")})
		for i := 0; i < 100; i++ {
			l.routeFrame(&Frame{Opcode: OpcodeContinuation})
		}
		l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("!")})

		if called != 2 {
			t.Errorf("Data called %d times, want 2", called)
		}
	})
}

/*
resetCurrentDataFrames reopens the assembly state for the next message.

The fresh slice is the part worth pinning: reslicing to [:0] would zero the
length while keeping the array, so the next message's frames would land on top
of the ones a hook is still holding.
*/
func TestListenerResetCurrentDataFrames(t *testing.T) {
	t.Run("zeroes every field", func(t *testing.T) {
		l := &Listener{
			configLocker:           &sync.Mutex{},
			currentDataFrames:      Frames{{Opcode: OpcodeText, PayloadData: []byte("he")}},
			currentDataFrameCount:  1,
			currentDataAccLength:   2,
			currentDataFrameOpcode: OpcodeText,
		}

		l.resetCurrentDataFrames()

		// 0 is OpcodeContinuation, which no message can be, so it reads as "none
		// open" — and GetCurrentMsgOpcode hands it to the caller as exactly that.
		if l.currentDataFrameOpcode != OpcodeContinuation {
			t.Errorf("currentDataFrameOpcode = %#x, want 0", l.currentDataFrameOpcode)
		}

		if len(l.currentDataFrames) != 0 {
			t.Errorf("currentDataFrames = %d, want 0", len(l.currentDataFrames))
		}
		// Both totals are budgets spent by the message just finished, so leaving
		// either behind charges the next message for it.
		if l.currentDataFrameCount != 0 {
			t.Errorf("currentDataFrameCount = %d, want 0", l.currentDataFrameCount)
		}
		if l.currentDataAccLength != 0 {
			t.Errorf("currentDataAccLength = %d, want 0", l.currentDataAccLength)
		}
		// Empty, not nil: everything else reads emptiness as "no message open"
		// and appends without checking.
		if l.currentDataFrames == nil {
			t.Error("currentDataFrames should be empty, not nil")
		}
	})

	t.Run("a fresh slice, not the old array", func(t *testing.T) {
		held := Frames{{Opcode: OpcodeText, PayloadData: []byte("first")}}
		l := &Listener{configLocker: &sync.Mutex{}, currentDataFrames: held, currentDataAccLength: 5}

		l.resetCurrentDataFrames()
		l.currentDataFrames = append(l.currentDataFrames, &Frame{
			Opcode: OpcodeText, PayloadData: []byte("second"),
		})

		if held.String() != "first" {
			t.Errorf("the frames held from before the reset became %q", held.String())
		}
	})
}

/*
RFC 6455 5.6 makes a text message UTF-8 as a whole, and 8.1 requires failing the
connection when it is not. Binary is exempt: its payload is arbitrary bytes.
*/
func TestListenerRouteFrameTextUTF8(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		called := 0
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Text = func(Frames) { called++ }

		err := l.routeFrame(&Frame{Opcode: OpcodeText, FIN: true, PayloadData: []byte("中文")})

		if err != nil {
			t.Errorf("err = %v, want nil", err)
		}
		if called != 1 {
			t.Errorf("Text called %d times, want 1", called)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		called := 0
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Text = func(Frames) { called++ }

		err := l.routeFrame(&Frame{Opcode: OpcodeText, FIN: true, PayloadData: []byte{0xFF, 0xFE}})

		if !errors.Is(err, ErrInvalidUTF8) {
			t.Errorf("err = %v, want ErrInvalidUTF8", err)
		}
		if called != 0 {
			t.Error("a message that is not UTF-8 must not reach Text")
		}
	})

	// 5.6 again: a frame may end halfway through a rune, so the bytes are only
	// judged once joined. Per frame these are 0xE4 0xB8 and 0xAD, both invalid.
	t.Run("rune split across frames", func(t *testing.T) {
		var got Frames
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Text = func(frames Frames) { got = frames }

		l.routeFrame(&Frame{Opcode: OpcodeText, PayloadData: []byte{0xE4, 0xB8}})
		err := l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte{0xAD}})

		if err != nil {
			t.Fatalf("err = %v, want nil — the joined bytes are 中", err)
		}
		if got.String() != "中" {
			t.Errorf("Text got %q, want 中", got.String())
		}
	})

	// The invalid half only appears once the frames are joined, so a per frame
	// check would let this through.
	t.Run("invalid only once joined", func(t *testing.T) {
		called := 0
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Text = func(Frames) { called++ }

		l.routeFrame(&Frame{Opcode: OpcodeText, PayloadData: []byte{0xE4, 0xB8}})
		err := l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte{0x41}})

		if !errors.Is(err, ErrInvalidUTF8) {
			t.Errorf("err = %v, want ErrInvalidUTF8", err)
		}
		if called != 0 {
			t.Error("a message that is not UTF-8 must not reach Text")
		}
	})

	// 5.6: binary is arbitrary bytes, so validating it would break every
	// protobuf, image and compressed payload.
	t.Run("Binary is not checked", func(t *testing.T) {
		var got Frames
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Binary = func(frames Frames) { got = frames }

		err := l.routeFrame(&Frame{Opcode: OpcodeBinary, FIN: true, PayloadData: []byte{0xFF, 0xFE}})

		if err != nil {
			t.Errorf("err = %v, want nil", err)
		}
		if len(got) != 1 {
			t.Error("Binary should have received the message")
		}
	})
}

/*
The shape RFC 6455 5.5.1 gives a close body: absent, or a 2 byte status code
optionally followed by a UTF-8 reason.
*/
func TestListenerRouteFrameClosePayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		want    error
	}{
		{"no body", nil, nil},
		{"status code only", []byte{0x03, 0xE8}, nil},
		// The code in front of the reason is a 2 byte integer, not text: 0x03
		// 0xE8 is 1000 and is invalid UTF-8 on its own, so checking the whole
		// payload would refuse this.
		{"status code and reason", []byte{0x03, 0xE8, 'b', 'y', 'e'}, nil},
		{"one byte", []byte{0x03}, ErrClosePayloadTooShort},
		{"reason that is not UTF-8", []byte{0x03, 0xE8, 0xFF, 0xFE}, ErrInvalidUTF8},
		{"status code the RFC does not allow", closeBody(1006), ErrInvalidCloseStatusCode},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			called := 0
			l := &Listener{configLocker: &sync.Mutex{}}
			l.config.Close = func(*Frame) { called++ }

			err := l.routeFrame(&Frame{Opcode: OpcodeClose, FIN: true, PayloadData: testCase.payload})

			if !errors.Is(err, testCase.want) {
				t.Errorf("err = %v, want %v", err, testCase.want)
			}
			// A malformed close must not reach the hook, which would otherwise
			// decode it with GetClosePayload and get an error or a panic.
			wantCalled := 0
			if testCase.want == nil {
				wantCalled = 1
			}
			if called != wantCalled {
				t.Errorf("Close called %d times, want %d", called, wantCalled)
			}
		})
	}
}

func closeBody(statusCode uint16) []byte {
	return (&ClosePayload{StatusCode: statusCode}).Bytes()
}

/*
RFC 6455 7.4.2 splits the status code space and 7.4.1 keeps three of the
registered codes off the wire, so a peer sending one of those is violating the
spec however sensible the number looks.
*/
func TestValidCloseStatusCode(t *testing.T) {
	tests := []struct {
		name  string
		codes []uint16
		want  bool
	}{
		{"defined by RFC 6455", []uint16{1000, 1001, 1002, 1003, 1007, 1008, 1009, 1010, 1011}, true},
		// Absent from RFC 6455, registered with IANA afterwards through the
		// process 7.4.2 describes.
		{"registered later", []uint16{1012, 1013, 1014}, true},
		{"library and framework use", []uint16{3000, 3999}, true},
		{"private use", []uint16{4000, 4999}, true},

		{"unused range", []uint16{0, 1, 999}, false},
		{"reserved with no meaning", []uint16{1004}, false},
		// Local only: what an application reports to itself when the peer sent
		// no status, the connection died, or TLS failed.
		{"local only", []uint16{1005, 1006, 1015}, false},
		{"undefined in the protocol range", []uint16{1016, 1100, 2000, 2999}, false},
		{"above the private range", []uint16{5000, 65535}, false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			for _, code := range testCase.codes {
				if got := validCloseStatusCode(code); got != testCase.want {
					t.Errorf("validCloseStatusCode(%d) = %v, want %v", code, got, testCase.want)
				}
			}
		})
	}
}

/*
An empty continuation adds nothing to the message RFC 6455 5.4 defines as the
concatenation of its fragments, and MaxMsgPayloadByteLen counts payload bytes —
so retaining them would let a peer grow currentDataFrames without ever spending
budget.
*/
func TestListenerRouteFrameEmptyContinuation(t *testing.T) {
	t.Run("dropped", func(t *testing.T) {
		var got Frames
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Text = func(frames Frames) { got = frames }

		l.routeFrame(&Frame{Opcode: OpcodeText, PayloadData: []byte("he")})
		for i := 0; i < 1000; i++ {
			l.routeFrame(&Frame{Opcode: OpcodeContinuation})
		}
		l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("llo")})

		if got.String() != "hello" {
			t.Errorf("Text got %q, want hello", got.String())
		}
		if len(got) != 2 {
			t.Errorf("kept %d frames, want 2 — the 1000 empty ones should be gone", len(got))
		}
	})

	// A frame with FIN ends the message whether or not it carries payload.
	t.Run("empty FIN still ends the message", func(t *testing.T) {
		var got Frames
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Text = func(frames Frames) { got = frames }

		l.routeFrame(&Frame{Opcode: OpcodeText, PayloadData: []byte("hi")})
		l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true})

		if got.String() != "hi" {
			t.Errorf("Text got %q, want hi", got.String())
		}
		if len(l.currentDataFrames) != 0 {
			t.Error("the message should have completed")
		}
	})

	// The first frame names the message type, so an empty one is kept — drop it
	// and the next continuation finds nothing open.
	t.Run("empty opening frame is kept", func(t *testing.T) {
		var got Frames
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.Text = func(frames Frames) { got = frames }

		if err := l.routeFrame(&Frame{Opcode: OpcodeText}); err != nil {
			t.Fatalf("routeFrame: %v", err)
		}
		if len(l.currentDataFrames) != 1 {
			t.Fatal("the opening frame must open the message")
		}
		l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("hi")})

		if got.String() != "hi" {
			t.Errorf("Text got %q, want hi", got.String())
		}
	})
}

/*
MaxMsgPayloadByteLen bounds the bytes a message carries, MaxMsgFrameCount the
frames it arrives in. Empty continuations are dropped and so never spend the
byte budget, which is why the second bound exists.
*/
func TestListenerRouteFrameMaxMsgFrameCount(t *testing.T) {
	t.Run("exactly the limit is allowed", func(t *testing.T) {
		var got Frames
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.MaxMsgFrameCount = 3
		l.config.Text = func(frames Frames) { got = frames }

		l.routeFrame(&Frame{Opcode: OpcodeText, PayloadData: []byte("a")})
		l.routeFrame(&Frame{Opcode: OpcodeContinuation, PayloadData: []byte("b")})
		err := l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("c")})

		if err != nil {
			t.Fatalf("3 frames against a limit of 3: %v", err)
		}
		if got.String() != "abc" {
			t.Errorf("Text got %q, want abc", got.String())
		}
	})

	t.Run("one over is refused", func(t *testing.T) {
		called := 0
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.MaxMsgFrameCount = 2
		l.config.Text = func(Frames) { called++ }

		l.routeFrame(&Frame{Opcode: OpcodeText, PayloadData: []byte("a")})
		l.routeFrame(&Frame{Opcode: OpcodeContinuation, PayloadData: []byte("b")})
		err := l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("c")})

		if !errors.Is(err, ErrMsgFrameCountExceeded) {
			t.Errorf("err = %v, want ErrMsgFrameCountExceeded", err)
		}
		if called != 0 {
			t.Error("a message over the frame limit must not reach its hook")
		}
		// It can never complete, so holding its frames serves nothing.
		if len(l.currentDataFrames) != 0 || l.currentDataFrameCount != 0 || l.currentDataAccLength != 0 {
			t.Error("the assembly state should have been reset")
		}
	})

	// An empty continuation never joins the message, so it must not spend the
	// frame budget either — currentDataFrameCount is counted on the way in, and
	// counting one here would refuse a message whose frames all fit.
	t.Run("empty continuations do not count", func(t *testing.T) {
		var got Frames
		l := &Listener{configLocker: &sync.Mutex{}}
		l.config.MaxMsgFrameCount = 2
		l.config.Text = func(frames Frames) { got = frames }

		l.routeFrame(&Frame{Opcode: OpcodeText, PayloadData: []byte("he")})
		for i := 0; i < 100; i++ {
			if err := l.routeFrame(&Frame{Opcode: OpcodeContinuation}); err != nil {
				t.Fatalf("empty continuation %d: %v", i, err)
			}
		}
		err := l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("llo")})

		if err != nil {
			t.Fatalf("2 counting frames against a limit of 2: %v", err)
		}
		if got.String() != "hello" {
			t.Errorf("Text got %q, want hello", got.String())
		}
	})

	// The count is kept beside the frames instead of read off them, so nothing
	// stops the two from drifting apart except this.
	t.Run("counts what is held", func(t *testing.T) {
		l := &Listener{configLocker: &sync.Mutex{}}

		l.routeFrame(&Frame{Opcode: OpcodeText, PayloadData: []byte("a")})
		l.routeFrame(&Frame{Opcode: OpcodeContinuation})    // dropped
		l.routeFrame(&Frame{Opcode: OpcodePing, FIN: true}) // never joins
		l.routeFrame(&Frame{Opcode: OpcodeContinuation, PayloadData: []byte("b")})

		if l.currentDataFrameCount != uint64(len(l.currentDataFrames)) {
			t.Errorf("currentDataFrameCount = %d, but %d frames are held",
				l.currentDataFrameCount, len(l.currentDataFrames))
		}
	})

	t.Run("0 means no limit", func(t *testing.T) {
		l := &Listener{configLocker: &sync.Mutex{}}
		l.routeFrame(&Frame{Opcode: OpcodeText, PayloadData: []byte("x")})
		for i := 0; i < 5000; i++ {
			if err := l.routeFrame(&Frame{Opcode: OpcodeContinuation, PayloadData: []byte("x")}); err != nil {
				t.Fatalf("refused at frame %d with no limit set: %v", i, err)
			}
		}
		if len(l.currentDataFrames) != 5001 {
			t.Errorf("held %d frames, want 5001", len(l.currentDataFrames))
		}
	})
}

// Control frames go straight to their hook and never join the message, so one
// arriving between two fragments spends nothing from MaxMsgFrameCount.
func TestListenerRouteFrameControlFramesDoNotCount(t *testing.T) {
	var got Frames
	l := &Listener{configLocker: &sync.Mutex{}}
	l.config.MaxMsgFrameCount = 2
	l.config.Text = func(frames Frames) { got = frames }
	l.config.Ping = func(*Frame) {}

	l.routeFrame(&Frame{Opcode: OpcodeText, PayloadData: []byte("he")})
	for i := 0; i < 100; i++ {
		if err := l.routeFrame(&Frame{Opcode: OpcodePing, FIN: true}); err != nil {
			t.Fatalf("ping %d: %v", i, err)
		}
	}
	err := l.routeFrame(&Frame{Opcode: OpcodeContinuation, FIN: true, PayloadData: []byte("llo")})

	if err != nil {
		t.Fatalf("2 data frames against a limit of 2: %v", err)
	}
	if got.String() != "hello" {
		t.Errorf("Text got %q, want hello", got.String())
	}
}
