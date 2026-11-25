package benchmarks

import (
	"bufio"
	"bytes"
	"io"
	"reflect"
	"testing"

	"github.com/weilun-shrimp/wlgows/v7"
)

func TestNewBenchmarkCases(t *testing.T) {
	want := []benchmarkCase{
		{
			id:         "Server-Send:Frame-One-16B",
			side:       serverSide,
			operation:  sendOperation,
			frameSize:  16,
			frameCount: 1,
		},
		{
			id:         "Server-Send:Frame-Many64-16B",
			side:       serverSide,
			operation:  sendOperation,
			frameSize:  16,
			frameCount: 64,
		},
		{
			id:         "Server-Send:Frame-One-1KB",
			side:       serverSide,
			operation:  sendOperation,
			frameSize:  1024,
			frameCount: 1,
		},
		{
			id:         "Server-Send:Frame-Many64-1KB",
			side:       serverSide,
			operation:  sendOperation,
			frameSize:  1024,
			frameCount: 64,
		},
		{
			id:         "Server-Send:Frame-One-16KB",
			side:       serverSide,
			operation:  sendOperation,
			frameSize:  16 * 1024,
			frameCount: 1,
		},
		{
			id:         "Server-Send:Frame-Many8-16KB",
			side:       serverSide,
			operation:  sendOperation,
			frameSize:  16 * 1024,
			frameCount: 8,
		},
		{
			id:         "Server-Send:Frame-One-1MB",
			side:       serverSide,
			operation:  sendOperation,
			frameSize:  1024 * 1024,
			frameCount: 1,
		},
		{
			id:         "Server-Send:Frame-Many8-1MB",
			side:       serverSide,
			operation:  sendOperation,
			frameSize:  1024 * 1024,
			frameCount: 8,
		},
		{
			id:         "Server-Read:Frame-One-16B",
			side:       serverSide,
			operation:  readOperation,
			frameSize:  16,
			frameCount: 1,
		},
		{
			id:         "Server-Read:Frame-Many64-16B",
			side:       serverSide,
			operation:  readOperation,
			frameSize:  16,
			frameCount: 64,
		},
		{
			id:         "Server-Read:Frame-One-1KB",
			side:       serverSide,
			operation:  readOperation,
			frameSize:  1024,
			frameCount: 1,
		},
		{
			id:         "Server-Read:Frame-Many64-1KB",
			side:       serverSide,
			operation:  readOperation,
			frameSize:  1024,
			frameCount: 64,
		},
		{
			id:         "Server-Read:Frame-One-16KB",
			side:       serverSide,
			operation:  readOperation,
			frameSize:  16 * 1024,
			frameCount: 1,
		},
		{
			id:         "Server-Read:Frame-Many8-16KB",
			side:       serverSide,
			operation:  readOperation,
			frameSize:  16 * 1024,
			frameCount: 8,
		},
		{
			id:         "Server-Read:Frame-One-1MB",
			side:       serverSide,
			operation:  readOperation,
			frameSize:  1024 * 1024,
			frameCount: 1,
		},
		{
			id:         "Server-Read:Frame-Many8-1MB",
			side:       serverSide,
			operation:  readOperation,
			frameSize:  1024 * 1024,
			frameCount: 8,
		},
		{
			id:         "Client-Send:Frame-One-16B",
			side:       clientSide,
			operation:  sendOperation,
			frameSize:  16,
			frameCount: 1,
		},
		{
			id:         "Client-Send:Frame-Many64-16B",
			side:       clientSide,
			operation:  sendOperation,
			frameSize:  16,
			frameCount: 64,
		},
		{
			id:         "Client-Send:Frame-One-1KB",
			side:       clientSide,
			operation:  sendOperation,
			frameSize:  1024,
			frameCount: 1,
		},
		{
			id:         "Client-Send:Frame-Many64-1KB",
			side:       clientSide,
			operation:  sendOperation,
			frameSize:  1024,
			frameCount: 64,
		},
		{
			id:         "Client-Send:Frame-One-16KB",
			side:       clientSide,
			operation:  sendOperation,
			frameSize:  16 * 1024,
			frameCount: 1,
		},
		{
			id:         "Client-Send:Frame-Many8-16KB",
			side:       clientSide,
			operation:  sendOperation,
			frameSize:  16 * 1024,
			frameCount: 8,
		},
		{
			id:         "Client-Send:Frame-One-1MB",
			side:       clientSide,
			operation:  sendOperation,
			frameSize:  1024 * 1024,
			frameCount: 1,
		},
		{
			id:         "Client-Send:Frame-Many8-1MB",
			side:       clientSide,
			operation:  sendOperation,
			frameSize:  1024 * 1024,
			frameCount: 8,
		},
		{
			id:         "Client-Read:Frame-One-16B",
			side:       clientSide,
			operation:  readOperation,
			frameSize:  16,
			frameCount: 1,
		},
		{
			id:         "Client-Read:Frame-Many64-16B",
			side:       clientSide,
			operation:  readOperation,
			frameSize:  16,
			frameCount: 64,
		},
		{
			id:         "Client-Read:Frame-One-1KB",
			side:       clientSide,
			operation:  readOperation,
			frameSize:  1024,
			frameCount: 1,
		},
		{
			id:         "Client-Read:Frame-Many64-1KB",
			side:       clientSide,
			operation:  readOperation,
			frameSize:  1024,
			frameCount: 64,
		},
		{
			id:         "Client-Read:Frame-One-16KB",
			side:       clientSide,
			operation:  readOperation,
			frameSize:  16 * 1024,
			frameCount: 1,
		},
		{
			id:         "Client-Read:Frame-Many8-16KB",
			side:       clientSide,
			operation:  readOperation,
			frameSize:  16 * 1024,
			frameCount: 8,
		},
		{
			id:         "Client-Read:Frame-One-1MB",
			side:       clientSide,
			operation:  readOperation,
			frameSize:  1024 * 1024,
			frameCount: 1,
		},
		{
			id:         "Client-Read:Frame-Many8-1MB",
			side:       clientSide,
			operation:  readOperation,
			frameSize:  1024 * 1024,
			frameCount: 8,
		},
	}
	if got := newBenchmarkCases(); !reflect.DeepEqual(got, want) {
		t.Errorf("newBenchmarkCases() = %+v, want %+v", got, want)
	}
}

func TestCasePayload(t *testing.T) {
	tests := []struct {
		name       string
		frameSize  int
		sender     side
		wantLength int
	}{
		{
			name:       "empty frame from a server",
			frameSize:  0,
			sender:     serverSide,
			wantLength: 0,
		},
		{
			name:       "a 1 byte frame from a server fits no payload",
			frameSize:  1,
			sender:     serverSide,
			wantLength: 0,
		},
		{
			name:       "16B from a server",
			frameSize:  16,
			sender:     serverSide,
			wantLength: 14,
		},
		{
			name:       "16B from a client",
			frameSize:  16,
			sender:     clientSide,
			wantLength: 10,
		},
		{
			name:       "128B from a server takes the 127 byte frame of a 125 byte payload",
			frameSize:  128,
			sender:     serverSide,
			wantLength: 125,
		},
		{
			name:       "1KB from a server",
			frameSize:  1024,
			sender:     serverSide,
			wantLength: 1020,
		},
		{
			name:       "1KB from a client",
			frameSize:  1024,
			sender:     clientSide,
			wantLength: 1016,
		},
		{
			name:       "16KB from a server",
			frameSize:  16 * 1024,
			sender:     serverSide,
			wantLength: 16380,
		},
		{
			name:       "16KB from a client",
			frameSize:  16 * 1024,
			sender:     clientSide,
			wantLength: 16376,
		},
		{
			name:       "65539B from a server takes the 65539 byte frame of a 65535 byte payload",
			frameSize:  65539,
			sender:     serverSide,
			wantLength: 65535,
		},
		{
			name:       "1MB from a server",
			frameSize:  1024 * 1024,
			sender:     serverSide,
			wantLength: 1048566,
		},
		{
			name:       "1MB from a client",
			frameSize:  1024 * 1024,
			sender:     clientSide,
			wantLength: 1048562,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := bytes.Repeat([]byte{'x'}, test.wantLength)
			if got := casePayload(test.frameSize, test.sender); !bytes.Equal(got, want) {
				t.Errorf("casePayload(%d, %s) is %d bytes, want %d bytes of x",
					test.frameSize, test.sender, len(got), test.wantLength)
			}
		})
	}
}

func TestRecordedWire(t *testing.T) {
	tests := []struct {
		name       string
		sender     side
		frameSize  int
		frameCount int
	}{
		{
			name:       "one 16B frame from a server",
			sender:     serverSide,
			frameSize:  16,
			frameCount: 1,
		},
		{
			name:       "64 16B frames from a client",
			sender:     clientSide,
			frameSize:  16,
			frameCount: 64,
		},
		{
			name:       "8 16KB frames from a client",
			sender:     clientSide,
			frameSize:  16 * 1024,
			frameCount: 8,
		},
		{
			name:       "8 1MB frames from a server",
			sender:     serverSide,
			frameSize:  1024 * 1024,
			frameCount: 8,
		},
		{
			name:       "8 1MB frames from a client",
			sender:     clientSide,
			frameSize:  1024 * 1024,
			frameCount: 8,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire, err := recordedWire(test.sender, test.frameSize, test.frameCount)
			if err != nil {
				t.Fatalf("recordedWire returned %v", err)
			}
			if len(wire) != test.frameSize*test.frameCount {
				t.Fatalf("the wire is %d bytes, want %d", len(wire), test.frameSize*test.frameCount)
			}

			type sentFrame struct {
				FIN         bool
				Opcode      byte
				Mask        bool
				PayloadData []byte
			}
			want := sentFrame{
				FIN:         true,
				Opcode:      wlgows.OpcodeBinary,
				Mask:        test.sender.masks(),
				PayloadData: casePayload(test.frameSize, test.sender),
			}
			reader := bufio.NewReader(bytes.NewReader(wire))
			for i := 0; i < test.frameCount; i++ {
				frame, err := wlgows.GetFrameHeaderFromReader(reader)
				if err != nil {
					t.Fatalf("frame %d: GetFrameHeaderFromReader returned %v", i, err)
				}
				if err := frame.FillFramePayloadFromReader(reader); err != nil {
					t.Fatalf("frame %d: FillFramePayloadFromReader returned %v", i, err)
				}
				got := sentFrame{
					FIN:         frame.FIN,
					Opcode:      frame.Opcode,
					Mask:        frame.Mask,
					PayloadData: frame.PayloadData,
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("frame %d: FIN %v, opcode %d, mask %v, %d payload bytes, "+
						"want FIN %v, opcode %d, mask %v, %d payload bytes",
						i, got.FIN, got.Opcode, got.Mask, len(got.PayloadData),
						want.FIN, want.Opcode, want.Mask, len(want.PayloadData))
				}
			}
			if _, err := reader.ReadByte(); err != io.EOF {
				t.Errorf("after %d frames the wire has more bytes, want io.EOF, got %v", test.frameCount, err)
			}
		})
	}
}
