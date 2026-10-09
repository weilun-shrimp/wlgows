package wlgows

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"testing"
)

// Every header shape, read through a real bufio.Reader. The byte after the
// frame must be the next one read: the frame was consumed exactly.
func TestGetFrameFromReaderParses(t *testing.T) {
	base_di := func() getFrameFromReaderDI {
		return getFrameFromReaderDI{ioReadFull: io.ReadFull, maskPayload: MaskPayload}
	}
	const next = 0xAA

	for _, testCase := range []struct {
		name    string
		header  []byte
		payload []byte
		want    Frame // PayloadData is set from payload
	}{
		{
			name:    "7 bit length, unmasked",
			header:  []byte{0x81, 0x02},
			payload: []byte("hi"),
			want:    Frame{FIN: true, Opcode: 1, PayloadLength: 2},
		},
		{
			name:    "7 bit length, masked",
			header:  []byte{0x81, 0x82, 1, 2, 3, 4},
			payload: []byte("hi"),
			want:    Frame{FIN: true, Opcode: 1, Mask: true, PayloadLength: 2, MaskingKey: [4]byte{1, 2, 3, 4}},
		},
		{
			name:    "16 bit length, masked",
			header:  []byte{0x82, 0xFE, 0x01, 0x2C, 5, 6, 7, 8},
			payload: bytes.Repeat([]byte{'a'}, 300),
			want: Frame{FIN: true, Opcode: 2, Mask: true, PayloadLength: 126, ExtendedPayloadLength: 300,
				MaskingKey: [4]byte{5, 6, 7, 8}},
		},
		{
			name:    "64 bit length, unmasked",
			header:  []byte{0x82, 0x7F, 0, 0, 0, 0, 0, 0x01, 0x11, 0x70},
			payload: bytes.Repeat([]byte{'b'}, 70000),
			want:    Frame{FIN: true, Opcode: 2, PayloadLength: 127, ExtendedPayloadLength: 70000},
		},
		{
			name:    "64 bit length, masked",
			header:  []byte{0x82, 0xFF, 0, 0, 0, 0, 0, 0x01, 0x11, 0x70, 9, 10, 11, 12},
			payload: bytes.Repeat([]byte{'c'}, 70000),
			want: Frame{FIN: true, Opcode: 2, Mask: true, PayloadLength: 127, ExtendedPayloadLength: 70000,
				MaskingKey: [4]byte{9, 10, 11, 12}},
		},
		{
			name:    "RSV bits, continuation, no FIN",
			header:  []byte{0x70, 0x01},
			payload: []byte{0xFF},
			want:    Frame{RSV1: true, RSV2: true, RSV3: true, Opcode: 0, PayloadLength: 1},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			wire := slices.Clone(testCase.header)
			for i, b := range testCase.payload {
				if testCase.want.Mask {
					b ^= testCase.want.MaskingKey[i&3]
				}
				wire = append(wire, b)
			}
			reader := bufio.NewReader(bytes.NewReader(append(wire, next)))
			testCase.want.PayloadData = testCase.payload

			got, err := getFrameFromReader(reader, 0, base_di())
			if err != nil {
				t.Fatalf("getFrameFromReader: %v", err)
			}
			if !reflect.DeepEqual(got, &testCase.want) {
				t.Errorf("frame %+v, want %+v", *got, testCase.want)
			}
			if b, err := reader.ReadByte(); err != nil || b != next {
				t.Errorf("next byte %#x, %v, want %#x: the frame was not read exactly", b, err, next)
			}
		})
	}
}

// The header is peeked whole before it is discarded, and the payload is read
// for the length the header declares.
func TestGetFrameFromReaderSteps(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		header []byte
		want   []string
	}{
		{
			name:   "7 bit length, unmasked",
			header: []byte{0x82, 0x05},
			want:   []string{"peek 2", "discard 2", "read 5"},
		},
		{
			name: "7 bit length, masked",
			header: []byte{
				0x82, 0x85,
				1, 2, 3, 4, // masking key
			},
			want: []string{"peek 2", "peek 6", "discard 6", "read 5", "mask 5 01 02 03 04"},
		},
		{
			name: "16 bit length, unmasked",
			header: []byte{
				0x82, 0x7E,
				0x01, 0x2C, // 300
			},
			want: []string{"peek 2", "peek 4", "discard 4", "read 300"},
		},
		{
			name: "64 bit length, masked",
			header: []byte{
				0x82, 0xFF,
				0, 0, 0, 0, 0, 0x01, 0x11, 0x70, // 70000
				1, 2, 3, 4, // masking key
			},
			want: []string{"peek 2", "peek 14", "discard 14", "read 70000", "mask 70000 01 02 03 04"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var steps []string
			reader := fakeGetFrameFromReaderBufioReader{
				peek: func(n int) ([]byte, error) {
					steps = append(steps, fmt.Sprintf("peek %d", n))
					return testCase.header[:n], nil
				},
				discard: func(n int) (int, error) {
					steps = append(steps, fmt.Sprintf("discard %d", n))
					return n, nil
				},
			}
			di := getFrameFromReaderDI{
				ioReadFull: func(_ io.Reader, buf []byte) (int, error) {
					steps = append(steps, fmt.Sprintf("read %d", len(buf)))
					return len(buf), nil
				},
				maskPayload: func(payload []byte, maskingKey [4]byte) {
					steps = append(steps, fmt.Sprintf("mask %d % x", len(payload), maskingKey))
				},
			}

			if _, err := getFrameFromReader(reader, 0, di); err != nil {
				t.Fatalf("getFrameFromReader: %v", err)
			}
			if !slices.Equal(steps, testCase.want) {
				t.Errorf("steps %q, want %q", steps, testCase.want)
			}
		})
	}
}

func TestGetFrameFromReaderErrors(t *testing.T) {
	base_di := func() getFrameFromReaderDI {
		return getFrameFromReaderDI{
			ioReadFull: func(_ io.Reader, buf []byte) (int, error) {
				return len(buf), nil
			},
		}
	}
	errRead := errors.New("read failed")

	t.Run("stream ends before the frame: io.EOF", func(t *testing.T) {
		reader := fakeGetFrameFromReaderBufioReader{
			peek: func(int) ([]byte, error) {
				return nil, io.EOF
			},
		}
		if _, err := getFrameFromReader(reader, 0, base_di()); !errors.Is(err, io.EOF) {
			t.Errorf("err = %v, want io.EOF", err)
		}
	})

	t.Run("stream ends after 1 header byte: io.ErrUnexpectedEOF", func(t *testing.T) {
		reader := fakeGetFrameFromReaderBufioReader{
			peek: func(int) ([]byte, error) {
				return []byte{0x82}, io.EOF
			},
		}
		if _, err := getFrameFromReader(reader, 0, base_di()); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("err = %v, want io.ErrUnexpectedEOF", err)
		}
	})

	t.Run("first peek fails", func(t *testing.T) {
		reader := fakeGetFrameFromReaderBufioReader{
			peek: func(int) ([]byte, error) {
				return nil, errRead
			},
		}
		if _, err := getFrameFromReader(reader, 0, base_di()); !errors.Is(err, errRead) {
			t.Errorf("err = %v, want %v", err, errRead)
		}
	})

	t.Run("stream ends inside the extended length: io.ErrUnexpectedEOF", func(t *testing.T) {
		peekCalls := 0
		reader := fakeGetFrameFromReaderBufioReader{
			peek: func(int) ([]byte, error) {
				peekCalls++
				if peekCalls == 2 {
					return []byte{0x82, 0x7E}, io.EOF
				}
				return []byte{0x82, 0x7E}, nil
			},
		}
		if _, err := getFrameFromReader(reader, 0, base_di()); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("err = %v, want io.ErrUnexpectedEOF", err)
		}
	})

	t.Run("second peek fails", func(t *testing.T) {
		peekCalls := 0
		reader := fakeGetFrameFromReaderBufioReader{
			peek: func(int) ([]byte, error) {
				peekCalls++
				if peekCalls == 2 {
					return nil, errRead
				}
				return []byte{0x82, 0x85}, nil
			},
		}
		if _, err := getFrameFromReader(reader, 0, base_di()); !errors.Is(err, errRead) {
			t.Errorf("err = %v, want %v", err, errRead)
		}
	})

	t.Run("64 bit length with its most significant bit set", func(t *testing.T) {
		reader := fakeGetFrameFromReaderBufioReader{
			peek: func(n int) ([]byte, error) {
				return []byte{0x82, 0x7F, 0x80, 0, 0, 0, 0, 0, 0, 0}[:n], nil
			},
		}
		if _, err := getFrameFromReader(reader, 0, base_di()); !errors.Is(err, ErrPayloadLengthMSBSet) {
			t.Errorf("err = %v, want ErrPayloadLengthMSBSet", err)
		}
	})

	t.Run("discard fails", func(t *testing.T) {
		reader := fakeGetFrameFromReaderBufioReader{
			peek: func(int) ([]byte, error) {
				return []byte{0x82, 0x05}, nil
			},
			discard: func(int) (int, error) {
				return 0, errRead
			},
		}
		if _, err := getFrameFromReader(reader, 0, base_di()); !errors.Is(err, errRead) {
			t.Errorf("err = %v, want %v", err, errRead)
		}
	})

	for _, testCase := range []struct {
		name    string
		readErr error
		want    error
	}{
		{"stream ends before the payload: io.ErrUnexpectedEOF", io.EOF, io.ErrUnexpectedEOF},
		{"stream ends inside the payload: io.ErrUnexpectedEOF", io.ErrUnexpectedEOF, io.ErrUnexpectedEOF},
		{"payload read fails", errRead, errRead},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reader := fakeGetFrameFromReaderBufioReader{
				peek: func(int) ([]byte, error) {
					return []byte{0x82, 0x05}, nil
				},
				discard: func(n int) (int, error) {
					return n, nil
				},
			}
			di := base_di()
			di.ioReadFull = func(io.Reader, []byte) (int, error) { return 0, testCase.readErr }
			if _, err := getFrameFromReader(reader, 0, di); !errors.Is(err, testCase.want) {
				t.Errorf("err = %v, want %v", err, testCase.want)
			}
		})
	}
}

// Reading a frame with an extended length allocates only the Frame and its
// payload, masked or not.
func TestGetFrameFromReaderAllocations(t *testing.T) {
	const runs = 100
	for _, testCase := range []struct {
		name          string
		payloadLength uint8
		length        uint64
		mask          bool
		maskingKey    [4]byte
	}{
		{"16 bit length, masked", 126, 300, true, [4]byte{1, 2, 3, 4}},
		{"16 bit length, unmasked", 126, 300, false, [4]byte{}},
		{"64 bit length, masked", 127, 70000, true, [4]byte{1, 2, 3, 4}},
		{"64 bit length, unmasked", 127, 70000, false, [4]byte{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			frame := append((&Frame{
				FIN: true, Opcode: 2, Mask: testCase.mask,
				PayloadLength: testCase.payloadLength, ExtendedPayloadLength: testCase.length,
				MaskingKey: testCase.maskingKey,
			}).appendSealedHeader(nil), make([]byte, testCase.length)...)
			// AllocsPerRun calls once more than runs, as a warm up.
			reader := bufio.NewReader(bytes.NewReader(bytes.Repeat(frame, runs+1)))

			allocs := testing.AllocsPerRun(runs, func() {
				if _, err := GetFrameFromReader(reader, 0); err != nil {
					t.Fatalf("GetFrameFromReader: %v", err)
				}
			})
			if allocs > 2 {
				t.Errorf("allocs per frame = %v, want <= 2", allocs)
			}
		})
	}
}

/*
A peer can claim a 10 GB payload in a 10 byte header. The guard must refuse
before the payload is allocated, so a refused frame never reaches ioReadFull.
*/
func TestGetFrameFromReaderMaxByteLength(t *testing.T) {
	// Declares 70000 bytes through the 64 bit extended length.
	header := []byte{0x82, 0x7F, 0, 0, 0, 0, 0, 0x01, 0x11, 0x70}

	for _, testCase := range []struct {
		name     string
		max      uint64
		wantErr  error
		wantRead []int
	}{
		{"0 means no limit", 0, nil, []int{70000}},
		{"exactly the max is allowed", 70000, nil, []int{70000}},
		{"one byte over the max is refused", 69999, ErrFrameByteLengthExceeded, nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var read []int
			reader := fakeGetFrameFromReaderBufioReader{
				peek:    func(n int) ([]byte, error) { return header[:n], nil },
				discard: func(n int) (int, error) { return n, nil },
			}
			di := getFrameFromReaderDI{
				ioReadFull: func(_ io.Reader, buf []byte) (int, error) {
					read = append(read, len(buf))
					return len(buf), nil
				},
			}

			_, err := getFrameFromReader(reader, testCase.max, di)
			if !errors.Is(err, testCase.wantErr) {
				t.Errorf("err = %v, want %v", err, testCase.wantErr)
			}
			if !slices.Equal(read, testCase.wantRead) {
				t.Errorf("payload reads %v, want %v", read, testCase.wantRead)
			}
		})
	}
}

func TestFrameReadErr(t *testing.T) {
	errRead := errors.New("read failed")
	for _, testCase := range []struct {
		name           string
		frameBytesRead int
		err            error
		want           error
	}{
		{"io.EOF before the frame stays io.EOF", 0, io.EOF, io.EOF},
		{"io.EOF inside the frame is io.ErrUnexpectedEOF", 1, io.EOF, io.ErrUnexpectedEOF},
		{"any other error is returned as is", 1, errRead, errRead},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := frameReadErr(testCase.frameBytesRead, testCase.err); got != testCase.want {
				t.Errorf("frameReadErr(%d, %v) = %v, want %v",
					testCase.frameBytesRead, testCase.err, got, testCase.want)
			}
		})
	}
}
