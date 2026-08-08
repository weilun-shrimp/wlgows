package wlgows

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
)

func TestReadTCPConnInjected(t *testing.T) {
	t.Run("fills the buffer from ioReadFull", func(t *testing.T) {
		got, err := readTCPConn(newFakeConn(nil), 4, readTCPConnDI{
			ioReadFull: func(r io.Reader, buf []byte) (int, error) {
				copy(buf, []byte{1, 2, 3, 4})
				return len(buf), nil
			},
		})
		if err != nil {
			t.Fatalf("readTCPConn: %v", err)
		}
		if !bytes.Equal(got, []byte{1, 2, 3, 4}) {
			t.Errorf("got % x, want 01 02 03 04", got)
		}
	})

	t.Run("propagates the ioReadFull error", func(t *testing.T) {
		want := errors.New("short read")
		got, err := readTCPConn(newFakeConn(nil), 4, readTCPConnDI{
			ioReadFull: func(io.Reader, []byte) (int, error) { return 0, want },
		})
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
		// The partially filled buffer is still returned alongside the error.
		if len(got) != 4 {
			t.Errorf("len(got) = %d, want 4", len(got))
		}
	})

	t.Run("allocates exactly maxLen", func(t *testing.T) {
		var seen int
		_, _ = readTCPConn(newFakeConn(nil), 7, readTCPConnDI{
			ioReadFull: func(_ io.Reader, buf []byte) (int, error) {
				seen = len(buf)
				return len(buf), nil
			},
		})
		if seen != 7 {
			t.Errorf("buffer len = %d, want 7", seen)
		}
	})
}

func TestReadTCPConnRealWiring(t *testing.T) {
	conn := newFakeConn([]byte("hello world"))
	got, err := ReadTCPConn(conn, 5)
	if err != nil {
		t.Fatalf("ReadTCPConn: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}

	if _, err := ReadTCPConn(newFakeConn([]byte("ab")), 10); err == nil {
		t.Error("expected an error when the conn has fewer bytes than maxLen")
	}
}

func TestGetFrameFromTCPConnParsesWireBytes(t *testing.T) {
	tests := []struct {
		name  string
		wire  []byte
		check func(*testing.T, *Frame)
	}{
		{
			name: "unmasked text frame",
			wire: []byte{0x81, 0x02, 'h', 'i'},
			check: func(t *testing.T, f *Frame) {
				if !f.FIN || f.Opcode != 1 || f.Mask {
					t.Errorf("FIN=%v Opcode=%d Mask=%v", f.FIN, f.Opcode, f.Mask)
				}
				if f.PayloadLength != 2 || string(f.PayloadData) != "hi" {
					t.Errorf("PayloadLength=%d PayloadData=%q", f.PayloadLength, f.PayloadData)
				}
			},
		},
		{
			name: "masked frame is unmasked on read",
			wire: []byte{0x81, 0x82, 1, 2, 3, 4, 'h' ^ 1, 'i' ^ 2},
			check: func(t *testing.T, f *Frame) {
				if !f.Mask {
					t.Error("Mask should be true")
				}
				if !bytes.Equal(f.MaskingKey, []byte{1, 2, 3, 4}) {
					t.Errorf("MaskingKey = % x", f.MaskingKey)
				}
				// PayloadData is documented as always unmasked.
				if string(f.PayloadData) != "hi" {
					t.Errorf("PayloadData = %q, want %q", f.PayloadData, "hi")
				}
			},
		},
		{
			name: "rsv bits and continuation frame",
			wire: []byte{0x72, 0x01, 0xFF},
			check: func(t *testing.T, f *Frame) {
				if f.FIN {
					t.Error("FIN should be false")
				}
				if !f.RSV1 || !f.RSV2 || !f.RSV3 {
					t.Errorf("RSV1=%v RSV2=%v RSV3=%v", f.RSV1, f.RSV2, f.RSV3)
				}
				if f.Opcode != 2 {
					t.Errorf("Opcode = %d, want 2", f.Opcode)
				}
			},
		},
		{
			name: "126 reads a 2 byte extended length",
			wire: append([]byte{0x81, 0x7E, 0x01, 0x2C}, bytes.Repeat([]byte{'a'}, 300)...),
			check: func(t *testing.T, f *Frame) {
				if f.PayloadLength != 126 || f.ExtendedPayloadLength != 300 {
					t.Errorf("PayloadLength=%d Extended=%d", f.PayloadLength, f.ExtendedPayloadLength)
				}
				if len(f.PayloadData) != 300 {
					t.Errorf("len(PayloadData) = %d, want 300", len(f.PayloadData))
				}
			},
		},
		{
			name: "127 reads an 8 byte extended length",
			wire: append(
				[]byte{0x82, 0x7F, 0, 0, 0, 0, 0, 0x01, 0x11, 0x70},
				bytes.Repeat([]byte{'b'}, 70000)...,
			),
			check: func(t *testing.T, f *Frame) {
				if f.PayloadLength != 127 || f.ExtendedPayloadLength != 70000 {
					t.Errorf("PayloadLength=%d Extended=%d", f.PayloadLength, f.ExtendedPayloadLength)
				}
				if len(f.PayloadData) != 70000 {
					t.Errorf("len(PayloadData) = %d, want 70000", len(f.PayloadData))
				}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			f, err := GetFrameFromTCPConn(newFakeConn(testCase.wire), 0)
			if err != nil {
				t.Fatalf("GetFrameFromTCPConn: %v", err)
			}
			testCase.check(t, f)
		})
	}
}

// Seal and GetFrameFromTCPConn are inverses.
func TestFrameSealRoundTripsThroughGetFrameFromTCPConn(t *testing.T) {
	original := &Frame{
		FIN: true, Opcode: 1, Mask: true, PayloadLength: 5,
		MaskingKey: []byte{9, 8, 7, 6}, PayloadData: []byte("round"),
	}
	got, err := GetFrameFromTCPConn(newFakeConn(original.Seal()), 0)
	if err != nil {
		t.Fatalf("GetFrameFromTCPConn: %v", err)
	}
	if string(got.PayloadData) != "round" {
		t.Errorf("PayloadData = %q, want %q", got.PayloadData, "round")
	}
	if got.Opcode != original.Opcode || got.FIN != original.FIN || got.Mask != original.Mask {
		t.Errorf("header mismatch: %+v", got)
	}
}

func TestGetFrameFromTCPConnReadErrors(t *testing.T) {
	tests := []struct {
		name   string
		chunks [][]byte
	}{
		{"first two bytes fail", nil},
		{"16 bit extended length read fails", [][]byte{{0x81, 0x7E}}},
		{"64 bit extended length read fails", [][]byte{{0x81, 0x7F}}},
		{"masking key read fails", [][]byte{{0x81, 0x82}}},
		{"payload read fails", [][]byte{{0x81, 0x02}}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := getFrameFromTCPConn(newFakeConn(nil), 0, getFrameFromTCPConnDI{
				readTCPConn: scriptedReadTCPConn(testCase.chunks...),
			})
			if !errors.Is(err, io.EOF) {
				t.Errorf("err = %v, want io.EOF", err)
			}
		})
	}
}

func TestGetFrameFromTCPConnUsesInjectedReader(t *testing.T) {
	var lengths []uint64
	_, err := getFrameFromTCPConn(newFakeConn(nil), 0, getFrameFromTCPConnDI{
		readTCPConn: func(_ net.Conn, maxLen uint64) ([]byte, error) {
			lengths = append(lengths, maxLen)
			switch len(lengths) {
			case 1:
				return []byte{0x81, 0x82}, nil // masked, 2 byte payload
			case 2:
				return []byte{0, 0, 0, 0}, nil // masking key
			default:
				return []byte("hi"), nil // payload
			}
		},
	})
	if err != nil {
		t.Fatalf("getFrameFromTCPConn: %v", err)
	}
	want := []uint64{2, 4, 2}
	if len(lengths) != len(want) {
		t.Fatalf("read %d times %v, want %d %v", len(lengths), lengths, len(want), want)
	}
	for i := range want {
		if lengths[i] != want[i] {
			t.Errorf("read %d asked for %d bytes, want %d", i, lengths[i], want[i])
		}
	}
}

/*
The v3 guard. A peer can claim a 10 GB payload in a 10 byte header, and the
only useful place to refuse is between parsing that header and allocating for
it — readTCPConn's first act is make([]byte, maxLen), so a check after the fact
protects nothing.

These drive getFrameFromTCPConn through the di seam so the payload read can be
observed directly: the assertion is not merely that an error comes back, but
that the oversized read was never attempted.
*/
func TestGetFrameFromTCPConnMaxByteLength(t *testing.T) {
	// A header declaring 70000 bytes, forcing the 64 bit extended length path.
	// PayloadData is nil, so Seal emits the header and nothing else.
	const declared = 70000
	header := (&Frame{
		FIN: true, Opcode: 2, PayloadLength: 127, ExtendedPayloadLength: declared,
	}).Seal()

	// readTCPConn replaying that header, recording every length it is asked for.
	scriptedHeader := func(asked *[]uint64) func(net.Conn, uint64) ([]byte, error) {
		offset := 0
		return func(_ net.Conn, maxLen uint64) ([]byte, error) {
			*asked = append(*asked, maxLen)
			if offset+int(maxLen) > len(header) {
				// Past the header: this is the payload read the guard must prevent.
				return make([]byte, maxLen), nil
			}
			chunk := header[offset : offset+int(maxLen)]
			offset += int(maxLen)
			return chunk, nil
		}
	}

	t.Run("refuses a header over the max without reading the payload", func(t *testing.T) {
		var asked []uint64
		_, err := getFrameFromTCPConn(newFakeConn(nil), 1000, getFrameFromTCPConnDI{
			readTCPConn: scriptedHeader(&asked),
		})
		if err == nil {
			t.Fatal("a 70000 byte claim against a 1000 byte max must fail")
		}
		if !errors.Is(err, ErrFrameByteLengthExceeded) {
			t.Errorf("err = %v, want it to wrap ErrFrameByteLengthExceeded", err)
		}
		// The whole point: the payload read never happened.
		for _, length := range asked {
			if length == declared {
				t.Fatalf("readTCPConn was asked for the full %d bytes — the guard "+
					"ran after the allocation, which defeats it", declared)
			}
		}
	})

	t.Run("0 means no limit", func(t *testing.T) {
		var asked []uint64
		_, err := getFrameFromTCPConn(newFakeConn(nil), 0, getFrameFromTCPConnDI{
			readTCPConn: scriptedHeader(&asked),
		})
		if err != nil {
			t.Fatalf("0 should impose no limit, got %v", err)
		}
		if asked[len(asked)-1] != declared {
			t.Errorf("last read asked for %d bytes, want the full %d", asked[len(asked)-1], declared)
		}
	})

	// Boundary: the max is inclusive, so a payload of exactly max is allowed and
	// one byte more is not.
	t.Run("the max is inclusive", func(t *testing.T) {
		for _, testCase := range []struct {
			name    string
			max     uint64
			wantErr bool
		}{
			{"exactly the max is allowed", declared, false},
			{"one under the max is refused", declared - 1, true},
			{"well over the max is allowed", declared + 1, false},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				var asked []uint64
				_, err := getFrameFromTCPConn(newFakeConn(nil), testCase.max, getFrameFromTCPConnDI{
					readTCPConn: scriptedHeader(&asked),
				})
				if testCase.wantErr && err == nil {
					t.Errorf("max=%d should have refused a %d byte claim", testCase.max, declared)
				}
				if !testCase.wantErr && err != nil {
					t.Errorf("max=%d should have allowed a %d byte claim, got %v", testCase.max, declared, err)
				}
			})
		}
	})
}
