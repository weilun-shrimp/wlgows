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
// header must be the next one read: the header was consumed exactly, and no
// payload was read or allocated.
func TestGetFrameHeaderFromReaderParses(t *testing.T) {
	const next = 0xAA

	for _, testCase := range []struct {
		name   string
		header []byte
		want   Frame
	}{
		{
			name:   "7 bit length, unmasked",
			header: []byte{0x81, 0x02},
			want:   Frame{FIN: true, Opcode: 1, PayloadLength: 2},
		},
		{
			name:   "7 bit length, masked",
			header: []byte{0x81, 0x82, 1, 2, 3, 4},
			want:   Frame{FIN: true, Opcode: 1, Mask: true, PayloadLength: 2, MaskingKey: [4]byte{1, 2, 3, 4}},
		},
		{
			name:   "16 bit length, masked",
			header: []byte{0x82, 0xFE, 0x01, 0x2C, 5, 6, 7, 8},
			want: Frame{FIN: true, Opcode: 2, Mask: true, PayloadLength: 126, ExtendedPayloadLength: 300,
				MaskingKey: [4]byte{5, 6, 7, 8}},
		},
		{
			name:   "64 bit length, unmasked",
			header: []byte{0x82, 0x7F, 0, 0, 0, 0, 0, 0x01, 0x11, 0x70},
			want:   Frame{FIN: true, Opcode: 2, PayloadLength: 127, ExtendedPayloadLength: 70000},
		},
		{
			name:   "64 bit length, masked",
			header: []byte{0x82, 0xFF, 0, 0, 0, 0, 0, 0x01, 0x11, 0x70, 9, 10, 11, 12},
			want: Frame{FIN: true, Opcode: 2, Mask: true, PayloadLength: 127, ExtendedPayloadLength: 70000,
				MaskingKey: [4]byte{9, 10, 11, 12}},
		},
		{
			name:   "RSV bits, continuation, no FIN",
			header: []byte{0x70, 0x01},
			want:   Frame{RSV1: true, RSV2: true, RSV3: true, Opcode: 0, PayloadLength: 1},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reader := bufio.NewReader(bytes.NewReader(append(slices.Clone(testCase.header), next)))

			got, err := getFrameHeaderFromReader(reader)
			if err != nil {
				t.Fatalf("getFrameHeaderFromReader: %v", err)
			}
			if !reflect.DeepEqual(got, &testCase.want) {
				t.Errorf("frame %+v, want %+v", *got, testCase.want)
			}
			if b, err := reader.ReadByte(); err != nil || b != next {
				t.Errorf("next byte %#x, %v, want %#x: the header was not read exactly", b, err, next)
			}
		})
	}
}

// The header is peeked whole before it is discarded, and nothing else is read.
func TestGetFrameHeaderFromReaderSteps(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		header []byte
		want   []string
	}{
		{
			name:   "7 bit length, unmasked",
			header: []byte{0x82, 0x05},
			want:   []string{"peek 2", "discard 2"},
		},
		{
			name: "7 bit length, masked",
			header: []byte{
				0x82, 0x85,
				1, 2, 3, 4, // masking key
			},
			want: []string{"peek 2", "peek 6", "discard 6"},
		},
		{
			name: "16 bit length, unmasked",
			header: []byte{
				0x82, 0x7E,
				0x01, 0x2C, // 300
			},
			want: []string{"peek 2", "peek 4", "discard 4"},
		},
		{
			name: "64 bit length, masked",
			header: []byte{
				0x82, 0xFF,
				0, 0, 0, 0, 0, 0x01, 0x11, 0x70, // 70000
				1, 2, 3, 4, // masking key
			},
			want: []string{"peek 2", "peek 14", "discard 14"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var steps []string
			reader := fakeGetFrameHeaderFromReaderBufioReader{
				peek: func(n int) ([]byte, error) {
					steps = append(steps, fmt.Sprintf("peek %d", n))
					return testCase.header[:n], nil
				},
				discard: func(n int) (int, error) {
					steps = append(steps, fmt.Sprintf("discard %d", n))
					return n, nil
				},
			}

			if _, err := getFrameHeaderFromReader(reader); err != nil {
				t.Fatalf("getFrameHeaderFromReader: %v", err)
			}
			if !slices.Equal(steps, testCase.want) {
				t.Errorf("steps %q, want %q", steps, testCase.want)
			}
		})
	}
}

func TestGetFrameHeaderFromReaderErrors(t *testing.T) {
	errRead := errors.New("read failed")

	t.Run("stream ends before the frame: io.EOF", func(t *testing.T) {
		reader := fakeGetFrameHeaderFromReaderBufioReader{
			peek: func(int) ([]byte, error) {
				return nil, io.EOF
			},
		}
		if _, err := getFrameHeaderFromReader(reader); !errors.Is(err, io.EOF) {
			t.Errorf("err = %v, want io.EOF", err)
		}
	})

	t.Run("stream ends after 1 header byte: io.ErrUnexpectedEOF", func(t *testing.T) {
		reader := fakeGetFrameHeaderFromReaderBufioReader{
			peek: func(int) ([]byte, error) {
				return []byte{0x82}, io.EOF
			},
		}
		if _, err := getFrameHeaderFromReader(reader); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("err = %v, want io.ErrUnexpectedEOF", err)
		}
	})

	t.Run("first peek fails", func(t *testing.T) {
		reader := fakeGetFrameHeaderFromReaderBufioReader{
			peek: func(int) ([]byte, error) {
				return nil, errRead
			},
		}
		if _, err := getFrameHeaderFromReader(reader); !errors.Is(err, errRead) {
			t.Errorf("err = %v, want %v", err, errRead)
		}
	})

	t.Run("stream ends inside the extended length: io.ErrUnexpectedEOF", func(t *testing.T) {
		peekCalls := 0
		reader := fakeGetFrameHeaderFromReaderBufioReader{
			peek: func(int) ([]byte, error) {
				peekCalls++
				if peekCalls == 2 {
					return []byte{0x82, 0x7E}, io.EOF
				}
				return []byte{0x82, 0x7E}, nil
			},
		}
		if _, err := getFrameHeaderFromReader(reader); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("err = %v, want io.ErrUnexpectedEOF", err)
		}
	})

	t.Run("second peek fails", func(t *testing.T) {
		peekCalls := 0
		reader := fakeGetFrameHeaderFromReaderBufioReader{
			peek: func(int) ([]byte, error) {
				peekCalls++
				if peekCalls == 2 {
					return nil, errRead
				}
				return []byte{0x82, 0x85}, nil
			},
		}
		if _, err := getFrameHeaderFromReader(reader); !errors.Is(err, errRead) {
			t.Errorf("err = %v, want %v", err, errRead)
		}
	})

	t.Run("64 bit length with its most significant bit set", func(t *testing.T) {
		reader := fakeGetFrameHeaderFromReaderBufioReader{
			peek: func(n int) ([]byte, error) {
				return []byte{0x82, 0x7F, 0x80, 0, 0, 0, 0, 0, 0, 0}[:n], nil
			},
		}
		if _, err := getFrameHeaderFromReader(reader); !errors.Is(err, ErrPayloadLengthMSBSet) {
			t.Errorf("err = %v, want ErrPayloadLengthMSBSet", err)
		}
	})

	t.Run("16 bit length under 126", func(t *testing.T) {
		reader := fakeGetFrameHeaderFromReaderBufioReader{
			peek: func(n int) ([]byte, error) {
				return []byte{0x82, 0x7E, 0x00, 0x7D}[:n], nil
			},
		}
		if _, err := getFrameHeaderFromReader(reader); !errors.Is(err, ErrPayloadLengthNotMinimal) {
			t.Errorf("err = %v, want ErrPayloadLengthNotMinimal", err)
		}
	})

	t.Run("64 bit length under 65536", func(t *testing.T) {
		reader := fakeGetFrameHeaderFromReaderBufioReader{
			peek: func(n int) ([]byte, error) {
				return []byte{0x82, 0x7F, 0, 0, 0, 0, 0, 0, 0xFF, 0xFF}[:n], nil
			},
		}
		if _, err := getFrameHeaderFromReader(reader); !errors.Is(err, ErrPayloadLengthNotMinimal) {
			t.Errorf("err = %v, want ErrPayloadLengthNotMinimal", err)
		}
	})

	t.Run("discard fails", func(t *testing.T) {
		reader := fakeGetFrameHeaderFromReaderBufioReader{
			peek: func(int) ([]byte, error) {
				return []byte{0x82, 0x05}, nil
			},
			discard: func(int) (int, error) {
				return 0, errRead
			},
		}
		if _, err := getFrameHeaderFromReader(reader); !errors.Is(err, errRead) {
			t.Errorf("err = %v, want %v", err, errRead)
		}
	})
}

// Reading a header allocates only the Frame, whatever the length encoding.
func TestGetFrameHeaderFromReaderAllocations(t *testing.T) {
	const runs = 100
	for _, testCase := range []struct {
		name   string
		header []byte
	}{
		{
			name:   "16 bit length, masked",
			header: []byte{0x82, 0xFE, 0x01, 0x2C, 1, 2, 3, 4},
		},
		{
			name:   "64 bit length, unmasked",
			header: []byte{0x82, 0x7F, 0, 0, 0, 0, 0, 0x01, 0x11, 0x70},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// AllocsPerRun calls once more than runs, as a warm up.
			reader := bufio.NewReader(bytes.NewReader(bytes.Repeat(testCase.header, runs+1)))

			allocs := testing.AllocsPerRun(runs, func() {
				if _, err := GetFrameHeaderFromReader(reader); err != nil {
					t.Fatalf("GetFrameHeaderFromReader: %v", err)
				}
			})
			if allocs > 1 {
				t.Errorf("allocs per header = %v, want <= 1", allocs)
			}
		})
	}
}

// 5.5: a control frame over 125 bytes is refused by its header, before its
// payload is allocated.
func TestGetFrameHeaderFromReaderControlFramePayload(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		header  []byte
		wantErr error
	}{
		{
			name:    "a 125 byte ping is allowed",
			header:  []byte{0x89, 0x7D},
			wantErr: nil,
		},
		{
			name:    "a 126 byte ping is refused",
			header:  []byte{0x89, 0x7E, 0x00, 0x7E},
			wantErr: ErrControlFramePayloadTooLong,
		},
		{
			name:    "a close claiming 2^62 bytes is refused",
			header:  []byte{0x88, 0x7F, 0x40, 0, 0, 0, 0, 0, 0, 0},
			wantErr: ErrControlFramePayloadTooLong,
		},
		{
			name:    "a 126 byte binary frame is not a control frame",
			header:  []byte{0x82, 0x7E, 0x00, 0x7E},
			wantErr: nil,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reader := fakeGetFrameHeaderFromReaderBufioReader{
				peek:    func(n int) ([]byte, error) { return testCase.header[:n], nil },
				discard: func(n int) (int, error) { return n, nil },
			}

			if _, err := getFrameHeaderFromReader(reader); !errors.Is(err, testCase.wantErr) {
				t.Errorf("err = %v, want %v", err, testCase.wantErr)
			}
		})
	}
}

// The payload step allocates the length the header declared, reads it, and
// unmasks it when the frame is masked.
func TestFrameFillFramePayloadFromReader(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		frame *Frame
		want  []string
	}{
		{
			name:  "unmasked",
			frame: &Frame{Opcode: OpcodeBinary, PayloadLength: 5},
			want:  []string{"read 5"},
		},
		{
			name:  "masked",
			frame: &Frame{Opcode: OpcodeBinary, Mask: true, MaskingKey: [4]byte{1, 2, 3, 4}, PayloadLength: 5},
			want:  []string{"read 5", "mask 5 01 02 03 04"},
		},
		{
			name:  "64 bit length, masked",
			frame: &Frame{Opcode: OpcodeBinary, Mask: true, MaskingKey: [4]byte{1, 2, 3, 4}, PayloadLength: 127, ExtendedPayloadLength: 70000},
			want:  []string{"read 70000", "mask 70000 01 02 03 04"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var steps []string
			di := fillFramePayloadFromReaderDI{
				ioReadFull: func(_ io.Reader, buf []byte) (int, error) {
					steps = append(steps, fmt.Sprintf("read %d", len(buf)))
					return len(buf), nil
				},
				maskPayload: func(payload []byte, maskingKey [4]byte) {
					steps = append(steps, fmt.Sprintf("mask %d % x", len(payload), maskingKey))
				},
			}

			if err := testCase.frame.fillFramePayloadFromReader(nil, di); err != nil {
				t.Fatalf("fillFramePayloadFromReader: %v", err)
			}
			if !slices.Equal(steps, testCase.want) {
				t.Errorf("steps %q, want %q", steps, testCase.want)
			}
			if uint64(len(testCase.frame.PayloadData)) != testCase.frame.GetDeclaredPayloadSize() {
				t.Errorf("payload is %d bytes, want %d", len(testCase.frame.PayloadData), testCase.frame.GetDeclaredPayloadSize())
			}
		})
	}

	// The header was read, so a stream ending now cut the frame short.
	errRead := errors.New("read failed")
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
			di := fillFramePayloadFromReaderDI{
				ioReadFull: func(io.Reader, []byte) (int, error) { return 0, testCase.readErr },
			}
			frame := &Frame{Opcode: OpcodeBinary, FIN: true, PayloadLength: 5}
			if err := frame.fillFramePayloadFromReader(nil, di); !errors.Is(err, testCase.want) {
				t.Errorf("err = %v, want %v", err, testCase.want)
			}
		})
	}
}

// Filling a payload allocates only the payload, masked or not.
func TestFrameFillFramePayloadFromReaderAllocations(t *testing.T) {
	const runs = 100
	for _, testCase := range []struct {
		name  string
		frame Frame
	}{
		{
			name:  "masked",
			frame: Frame{Opcode: OpcodeBinary, Mask: true, MaskingKey: [4]byte{1, 2, 3, 4}, PayloadLength: 127, ExtendedPayloadLength: 70000},
		},
		{
			name:  "unmasked",
			frame: Frame{Opcode: OpcodeBinary, PayloadLength: 127, ExtendedPayloadLength: 70000},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// AllocsPerRun calls once more than runs, as a warm up.
			reader := bufio.NewReader(bytes.NewReader(make([]byte, 70000*(runs+1))))
			frame := testCase.frame

			allocs := testing.AllocsPerRun(runs, func() {
				if err := frame.FillFramePayloadFromReader(reader); err != nil {
					t.Fatalf("FillFramePayloadFromReader: %v", err)
				}
			})
			if allocs > 1 {
				t.Errorf("allocs per payload = %v, want <= 1", allocs)
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
