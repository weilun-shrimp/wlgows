package wlgows

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
)

func TestMsgGetStr(t *testing.T) {
	t.Run("empty message", func(t *testing.T) {
		msg := Msg{}
		if got := msg.GetStr(); got != "" {
			t.Errorf("GetStr() = %q, want empty", got)
		}
	})

	t.Run("concatenates every frame payload", func(t *testing.T) {
		msg := Msg{Frames: []*Frame{
			{PayloadData: []byte("hello ")},
			{PayloadData: []byte("wl")},
			{PayloadData: []byte("gows")},
		}}
		if got := msg.GetStr(); got != "hello wlgows" {
			t.Errorf("GetStr() = %q, want %q", got, "hello wlgows")
		}
	})

	// The whole reason GetStr uses strings.Builder over string([]byte) per
	// frame: a multi-byte rune may straddle a frame boundary.
	t.Run("rejoins a utf-8 rune split across frames", func(t *testing.T) {
		msg := Msg{Frames: []*Frame{
			{PayloadData: []byte{0xE4, 0xB8}}, // first 2 bytes of 中
			{PayloadData: []byte{0xAD}},       // last byte of 中
		}}
		if got := msg.GetStr(); got != "中" {
			t.Errorf("GetStr() = %q, want %q", got, "中")
		}
	})
}

func TestGetMsgFromTCPConn(t *testing.T) {
	t.Run("collects frames until FIN", func(t *testing.T) {
		frames := []*Frame{
			{FIN: false, PayloadData: []byte("a")},
			{FIN: false, PayloadData: []byte("b")},
			{FIN: true, PayloadData: []byte("c")},
			{FIN: true, PayloadData: []byte("NOT READ")},
		}
		i := 0
		msg, err := getMsgFromTCPConn(newFakeConn(nil), getMsgFromTCPConnDI{
			getFrameFromTCPConn: func(net.Conn) (*Frame, error) {
				frame := frames[i]
				i++
				return frame, nil
			},
		})
		if err != nil {
			t.Fatalf("getMsgFromTCPConn: %v", err)
		}
		if len(msg.Frames) != 3 {
			t.Fatalf("len(Frames) = %d, want 3", len(msg.Frames))
		}
		if msg.GetStr() != "abc" {
			t.Errorf("GetStr() = %q, want %q", msg.GetStr(), "abc")
		}
	})

	t.Run("single final frame", func(t *testing.T) {
		msg, err := getMsgFromTCPConn(newFakeConn(nil), getMsgFromTCPConnDI{
			getFrameFromTCPConn: func(net.Conn) (*Frame, error) {
				return &Frame{FIN: true, PayloadData: []byte("solo")}, nil
			},
		})
		if err != nil {
			t.Fatalf("getMsgFromTCPConn: %v", err)
		}
		if len(msg.Frames) != 1 || msg.GetStr() != "solo" {
			t.Errorf("Frames=%d GetStr=%q", len(msg.Frames), msg.GetStr())
		}
	})

	t.Run("propagates a frame read error", func(t *testing.T) {
		want := errors.New("frame boom")
		_, err := getMsgFromTCPConn(newFakeConn(nil), getMsgFromTCPConnDI{
			getFrameFromTCPConn: func(net.Conn) (*Frame, error) { return nil, want },
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})

	t.Run("real wiring parses two frames off the wire", func(t *testing.T) {
		first := (&Frame{FIN: false, Opcode: 1, PayloadLength: 2, PayloadData: []byte("wl")}).Seal()
		last := (&Frame{FIN: true, Opcode: 0, PayloadLength: 4, PayloadData: []byte("gows")}).Seal()
		msg, err := GetMsgFromTCPConn(newFakeConn(append(first, last...)))
		if err != nil {
			t.Fatalf("GetMsgFromTCPConn: %v", err)
		}
		if msg.GetStr() != "wlgows" {
			t.Errorf("GetStr() = %q, want %q", msg.GetStr(), "wlgows")
		}
	})
}

func TestNewMsgLengthEncoding(t *testing.T) {
	tests := []struct {
		name         string
		size         int
		wantLen      byte
		wantExtended uint64
	}{
		{"125 bytes uses the 7 bit length", 125, 125, 0},
		{"126 bytes switches to the 16 bit length", 126, 126, 126},
		{"65535 bytes is the 16 bit ceiling", 65535, 126, 65535},
		{"65536 bytes switches to the 64 bit length", 65536, 127, 65536},
		{"1 byte", 1, 1, 0},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			msg, err := newMsg(bytes.Repeat([]byte{'x'}, testCase.size), 1, false, newMsgDI{
				generateMaskingKey: func() ([]byte, error) { return nil, nil },
			})
			if err != nil {
				t.Fatalf("newMsg: %v", err)
			}
			if len(msg.Frames) != 1 {
				t.Fatalf("len(Frames) = %d, want 1", len(msg.Frames))
			}
			frame := msg.Frames[0]
			if !frame.FIN {
				t.Error("FIN should be true for a single frame message")
			}
			if frame.PayloadLength != testCase.wantLen {
				t.Errorf("PayloadLength = %d, want %d", frame.PayloadLength, testCase.wantLen)
			}
			if frame.ExtendedPayloadLength != testCase.wantExtended {
				t.Errorf("ExtendedPayloadLength = %d, want %d", frame.ExtendedPayloadLength, testCase.wantExtended)
			}
			if uint64(len(frame.PayloadData)) != uint64(testCase.size) {
				t.Errorf("len(PayloadData) = %d, want %d", len(frame.PayloadData), testCase.size)
			}
		})
	}
}

// Empty data never enters the loop, so no frame is produced at all.
func TestNewMsgEmptyDataProducesNoFrames(t *testing.T) {
	msg, err := newMsg(nil, 1, false, newMsgDI{
		generateMaskingKey: func() ([]byte, error) { return nil, nil },
	})
	if err != nil {
		t.Fatalf("newMsg: %v", err)
	}
	if len(msg.Frames) != 0 {
		t.Errorf("len(Frames) = %d, want 0", len(msg.Frames))
	}
	if msg.GetStr() != "" {
		t.Errorf("GetStr() = %q, want empty", msg.GetStr())
	}
}

func TestNewMsgMasking(t *testing.T) {
	t.Run("masks with the injected key", func(t *testing.T) {
		msg, err := newMsg([]byte("hi"), 1, true, newMsgDI{
			generateMaskingKey: func() ([]byte, error) { return []byte{1, 2, 3, 4}, nil },
		})
		if err != nil {
			t.Fatalf("newMsg: %v", err)
		}
		frame := msg.Frames[0]
		if !frame.Mask {
			t.Error("Mask should be true")
		}
		if !bytes.Equal(frame.MaskingKey, []byte{1, 2, 3, 4}) {
			t.Errorf("MaskingKey = % x", frame.MaskingKey)
		}
		// A fixed key makes the sealed bytes fully assertable.
		want := []byte{0x81, 0x82, 1, 2, 3, 4, 'h' ^ 1, 'i' ^ 2}
		if got := frame.Seal(); !bytes.Equal(got, want) {
			t.Errorf("Seal() = % x, want % x", got, want)
		}
	})

	t.Run("leaves the frame unmasked when not requested", func(t *testing.T) {
		msg, err := newMsg([]byte("hi"), 1, false, newMsgDI{
			generateMaskingKey: func() ([]byte, error) {
				t.Fatal("generateMaskingKey must not be called when need_mask is false")
				return nil, nil
			},
		})
		if err != nil {
			t.Fatalf("newMsg: %v", err)
		}
		if msg.Frames[0].Mask || msg.Frames[0].MaskingKey != nil {
			t.Error("frame should be unmasked with no key")
		}
	})

	t.Run("propagates a masking key error", func(t *testing.T) {
		want := errors.New("no entropy")
		_, err := newMsg([]byte("hi"), 1, true, newMsgDI{
			generateMaskingKey: func() ([]byte, error) { return nil, want },
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestNewMsgOpcode(t *testing.T) {
	for _, opcode := range []uint8{1, 2, 8, 9, 10} {
		msg, err := newMsg([]byte("x"), opcode, false, newMsgDI{
			generateMaskingKey: func() ([]byte, error) { return nil, nil },
		})
		if err != nil {
			t.Fatalf("newMsg: %v", err)
		}
		if msg.Frames[0].Opcode != opcode {
			t.Errorf("Opcode = %d, want %d", msg.Frames[0].Opcode, opcode)
		}
	}
}

func TestGenerateMaskingKey(t *testing.T) {
	t.Run("returns the injected bytes", func(t *testing.T) {
		got, err := generateMaskingKey(generateMaskingKeyDI{
			randRead: fixedRandRead(0xAA, 0xBB, 0xCC, 0xDD),
		})
		if err != nil {
			t.Fatalf("generateMaskingKey: %v", err)
		}
		if !bytes.Equal(got, []byte{0xAA, 0xBB, 0xCC, 0xDD}) {
			t.Errorf("got % x", got)
		}
	})

	t.Run("propagates the rand error", func(t *testing.T) {
		want := errors.New("entropy exhausted")
		got, err := generateMaskingKey(generateMaskingKeyDI{
			randRead: func([]byte) (int, error) { return 0, want },
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
		if got != nil {
			t.Errorf("key should be nil on error, got % x", got)
		}
	})

	t.Run("real wiring returns the 4 bytes RFC 6455 requires", func(t *testing.T) {
		got, err := GenerateMaskingKey()
		if err != nil {
			t.Fatalf("GenerateMaskingKey: %v", err)
		}
		if len(got) != 4 {
			t.Errorf("len = %d, want 4", len(got))
		}
	})
}

// Guards the reader against a truncated stream.
func TestGetMsgFromTCPConnTruncatedStream(t *testing.T) {
	partial := (&Frame{FIN: true, Opcode: 1, PayloadLength: 10, PayloadData: []byte("short")}).Seal()
	_, err := GetMsgFromTCPConn(newFakeConn(partial[:4]))
	if err == nil {
		t.Fatal("expected an error on a truncated frame")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want an EOF variant", err)
	}
}
