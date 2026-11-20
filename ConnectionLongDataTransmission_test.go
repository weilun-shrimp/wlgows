package wlgows

import (
	"bufio"
	"compress/flate"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestConnStartLongDataTransmission(t *testing.T) {
	base_conn := func() *Conn {
		netConn := newFakeConn(nil)
		conn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), NewConnConfig{PeerIsClient: true})
		return conn
	}

	// A refused Start must leave the lock untaken, or the deferred Release a
	// caller writes after checking the error would unlock what nobody holds.
	t.Run("refuses an opcode that cannot open a message", func(t *testing.T) {
		for _, testCase := range []struct {
			opcode byte
			want   error
		}{
			// 5.4: there is nothing for it to continue.
			{OpcodeContinuation, ErrContinuationFrameWithoutMsg},
			// 5.5: a control frame is never fragmented.
			{OpcodeClose, ErrNotDataFrameOpcode},
			{OpcodePing, ErrNotDataFrameOpcode},
			{OpcodePong, ErrNotDataFrameOpcode},
			{0x3, ErrNotDataFrameOpcode}, // reserved by 5.2
		} {
			var steps []string
			conn := base_conn()
			conn.di.writeLocker = fakeFuncLocker{
				lock:   func() { steps = append(steps, "write lock") },
				unlock: func() { steps = append(steps, "write unlock") },
			}
			conn.di.dataFramesWriteLocker = fakeFuncLocker{
				lock:   func() { steps = append(steps, "data lock") },
				unlock: func() { steps = append(steps, "data unlock") },
			}

			if err := conn.StartLongDataTransmission(testCase.opcode, StartLongDataTransmissionConfig{}); !errors.Is(err, testCase.want) {
				t.Errorf("opcode %#x: err = %v, want %v", testCase.opcode, err, testCase.want)
			}
			if len(steps) != 0 || conn.currentTransmitDataMsgOpcode != 0 {
				t.Errorf("opcode %#x: steps %q, opcode %#x — want no lock taken and nothing opened",
					testCase.opcode, steps, conn.currentTransmitDataMsgOpcode)
			}
		}
	})

	// closeSent is read under the write lock, which is released before the
	// data lock is taken and held for the whole message.
	t.Run("claims the connection for a data opcode", func(t *testing.T) {
		for _, opcode := range []byte{OpcodeText, OpcodeBinary} {
			var steps []string
			conn := base_conn()
			conn.di.writeLocker = fakeFuncLocker{
				lock:   func() { steps = append(steps, "write lock") },
				unlock: func() { steps = append(steps, "write unlock") },
			}
			conn.di.dataFramesWriteLocker = fakeFuncLocker{
				lock:   func() { steps = append(steps, "data lock") },
				unlock: func() { steps = append(steps, "data unlock") },
			}

			if err := conn.StartLongDataTransmission(opcode, StartLongDataTransmissionConfig{}); err != nil {
				t.Fatalf("opcode %#x: %v", opcode, err)
			}
			if want := []string{"write lock", "write unlock", "data lock"}; !slices.Equal(steps, want) {
				t.Errorf("opcode %#x: steps %q, want %q", opcode, steps, want)
			}
			if conn.currentTransmitDataMsgOpcode != opcode {
				t.Errorf("opcode %#x: recorded %#x", opcode, conn.currentTransmitDataMsgOpcode)
			}
		}
	})

	// RFC 7692 5: nothing is deflated unless agreed.
	t.Run("deflates the message only when agreed and asked", func(t *testing.T) {
		for _, testCase := range []struct {
			name            string
			agreed          *PermessageDeflateConfig
			deflateIfAgreed bool
			want            bool
		}{
			{
				name:            "agreed and asked",
				agreed:          &PermessageDeflateConfig{},
				deflateIfAgreed: true,
				want:            true,
			},
			{
				name:            "agreed, not asked",
				agreed:          &PermessageDeflateConfig{},
				deflateIfAgreed: false,
				want:            false,
			},
			{
				name:            "asked, not agreed",
				agreed:          nil,
				deflateIfAgreed: true,
				want:            false,
			},
		} {
			conn := base_conn()
			conn.permessageDeflateAgreedConfig = testCase.agreed

			config := StartLongDataTransmissionConfig{DeflateIfAgreed: testCase.deflateIfAgreed}
			if err := conn.StartLongDataTransmission(OpcodeText, config); err != nil {
				t.Fatalf("%s: %v", testCase.name, err)
			}
			if conn.currentTransmitDataMsgDeflating != testCase.want {
				t.Errorf("%s: deflating %v, want %v", testCase.name, conn.currentTransmitDataMsgDeflating, testCase.want)
			}
		}
	})

	// 5.5.1: no message can open once a close has gone out.
	t.Run("refuses after a close", func(t *testing.T) {
		var steps []string
		conn := base_conn()
		conn.di.writeLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "write lock") },
			unlock: func() { steps = append(steps, "write unlock") },
		}
		conn.di.dataFramesWriteLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "data lock") },
			unlock: func() { steps = append(steps, "data unlock") },
		}
		conn.closeSent = true

		if err := conn.StartLongDataTransmission(OpcodeBinary, StartLongDataTransmissionConfig{}); !errors.Is(err, ErrCloseAlreadySent) {
			t.Errorf("err = %v, want ErrCloseAlreadySent", err)
		}
		if want := []string{"write lock", "write unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
		if conn.currentTransmitDataMsgOpcode != 0 {
			t.Error("a refused start opened a transmission")
		}
	})
}

func TestTransmitData(t *testing.T) {
	// base_di has a text transmission open on a real Conn, with
	// permessage-deflate agreed, and a sendFrame that succeeds; each case
	// overrides what it looks at. The message is not deflated unless a case
	// sets currentTransmitDataMsgDeflating, as Start would.
	base_di := func() transmitDataDI {
		netConn := newFakeConn(nil)
		conn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), NewConnConfig{PeerIsClient: true})
		conn.currentTransmitDataMsgOpcode = OpcodeText
		conn.permessageDeflateAgreedConfig = &PermessageDeflateConfig{}
		return transmitDataDI{
			conn:      conn,
			sendFrame: func(f *Frame) error { return nil },

			sendWindowBits:                func(*PermessageDeflateConfig, bool) uint { return 15 },
			validateDeflateLevelForWindow: func(int, uint) error { return nil },
		}
	}

	// 5.4: the message's opcode opens it, the rest continue it, and none of
	// them is the last. Masking is the send's, so the frame is built unmasked.
	t.Run("sends the fragment's frame", func(t *testing.T) {
		for _, opened := range []bool{false, true} {
			var sent []*Frame
			di := base_di()
			di.conn.currentTransmitDataMsgOpened = opened
			di.sendFrame = func(f *Frame) error {
				sent = append(sent, f)
				return nil
			}

			if err := transmitData([]byte("hello"), TransmitDataConfig{}, di); err != nil {
				t.Fatalf("transmitData: %v", err)
			}
			wantOpcode := uint8(OpcodeText)
			if opened {
				wantOpcode = OpcodeContinuation
			}
			if len(sent) != 1 || sent[0].Opcode != wantOpcode || sent[0].Mask ||
				string(sent[0].PayloadData) != "hello" || sent[0].FIN {
				t.Fatalf("opened %v: sent %+v, want one unmasked frame opcode %#x \"hello\" no FIN",
					opened, sent, wantOpcode)
			}
			if !di.conn.currentTransmitDataMsgOpened {
				t.Error("a sent fragment did not open the message")
			}
		}
	})

	// The write holds the lock closeSent is set under.
	t.Run("sends under the write lock", func(t *testing.T) {
		var steps []string
		di := base_di()
		di.conn.di.writeLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "write lock") },
			unlock: func() { steps = append(steps, "write unlock") },
		}
		di.sendFrame = func(*Frame) error {
			steps = append(steps, "send")
			return nil
		}

		if err := transmitData([]byte("hello"), TransmitDataConfig{}, di); err != nil {
			t.Fatalf("transmitData: %v", err)
		}
		if want := []string{"write lock", "send", "write unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	t.Run("refuses with no transmission open", func(t *testing.T) {
		di := base_di()
		di.conn.currentTransmitDataMsgOpcode = 0
		di.sendFrame = func(*Frame) error {
			t.Error("sent with no transmission open")
			return nil
		}

		if err := transmitData([]byte("hello"), TransmitDataConfig{}, di); !errors.Is(err, ErrLongDataTransmissionNotStarted) {
			t.Errorf("transmitData = %v, want ErrLongDataTransmissionNotStarted", err)
		}
	})

	t.Run("refuses an invalid deflate level before anything runs", func(t *testing.T) {
		// Go's compress/flate has no level -3 or 10. It only has -2 to 9.
		for _, level := range []int{-3, 10} {
			for _, deflating := range []bool{false, true} {
				var steps []string
				di := base_di()
				di.conn.currentTransmitDataMsgDeflating = deflating
				di.conn.di.newDataFrame = func(NewFrameConfig) (*Frame, error) {
					steps = append(steps, "build")
					return &Frame{}, nil
				}
				di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
					steps = append(steps, "deflate")
					return []byte{0x00, 0x00, 0xff, 0xff}, nil
				}}
				di.sendWindowBits = func(*PermessageDeflateConfig, bool) uint {
					steps = append(steps, "sendWindowBits")
					return 15
				}
				di.validateDeflateLevelForWindow = func(int, uint) error {
					steps = append(steps, "validateDeflateLevelForWindow")
					return nil
				}
				di.sendFrame = func(*Frame) error {
					steps = append(steps, "send")
					return nil
				}

				if err := transmitData([]byte("hello"), TransmitDataConfig{DeflateLevel: level}, di); !errors.Is(err, ErrDeflateLevelInvalid) {
					t.Errorf("level %d, deflating %v: transmitData = %v, want ErrDeflateLevelInvalid", level, deflating, err)
				}
				if len(steps) != 0 {
					t.Errorf("level %d, deflating %v: steps %q, want nothing run", level, deflating, steps)
				}
			}
		}
	})

	// RFC 7692 7.1.2: the window comes from this side's agreed config, and
	// the level is checked against it.
	small_windows := []struct {
		name         string
		agreed       PermessageDeflateConfig
		peerIsClient bool
	}{
		{
			name:         "server, server_max_window_bits=9",
			agreed:       PermessageDeflateConfig{ServerMaxWindowBits: 9},
			peerIsClient: true,
		},
		{
			name:         "client, client_max_window_bits=9",
			agreed:       PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 9},
			peerIsClient: false,
		},
	}

	t.Run("returns the validateDeflateLevelForWindow error and sends nothing", func(t *testing.T) {
		for _, testCase := range small_windows {
			var steps []string
			di := base_di()
			di.conn.peerIsClient = testCase.peerIsClient
			di.conn.permessageDeflateAgreedConfig = &testCase.agreed
			di.conn.currentTransmitDataMsgDeflating = true
			want_window_bits := uint(11)
			validate_deflate_level_for_window_error := errors.New("validateDeflateLevelForWindow error")
			di.sendWindowBits = func(agreed *PermessageDeflateConfig, peerIsClient bool) uint {
				steps = append(steps, fmt.Sprintf("sendWindowBits %+v %v", *agreed, peerIsClient))
				return want_window_bits
			}
			di.validateDeflateLevelForWindow = func(level int, window_bits uint) error {
				if window_bits != want_window_bits {
					t.Errorf(
						"%s: validateDeflateLevelForWindow got window_bits %d, want %d from sendWindowBits",
						testCase.name, window_bits, want_window_bits,
					)
				}
				steps = append(steps, fmt.Sprintf("validateDeflateLevelForWindow %d", level))
				return validate_deflate_level_for_window_error
			}
			di.conn.di.newDataFrame = func(NewFrameConfig) (*Frame, error) {
				steps = append(steps, "build")
				return &Frame{}, nil
			}
			di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
				steps = append(steps, "deflate")
				return []byte{0x00, 0x00, 0xff, 0xff}, nil
			}}
			di.sendFrame = func(*Frame) error {
				steps = append(steps, "send")
				return nil
			}

			config := TransmitDataConfig{DeflateLevel: flate.DefaultCompression}
			if err := transmitData([]byte("hello"), config, di); !errors.Is(err, validate_deflate_level_for_window_error) {
				t.Errorf("%s: transmitData = %v, want the validateDeflateLevelForWindow error", testCase.name, err)
			}
			want := []string{fmt.Sprintf("sendWindowBits %+v %v", testCase.agreed, testCase.peerIsClient), "validateDeflateLevelForWindow -1"}
			if !slices.Equal(steps, want) {
				t.Errorf("%s: steps %q, want %q", testCase.name, steps, want)
			}
			if di.conn.currentTransmitDataMsgOpened {
				t.Errorf("%s: a refused fragment opened the message", testCase.name)
			}
		}
	})

	t.Run("deflates and sends when validateDeflateLevelForWindow returns nil", func(t *testing.T) {
		for _, testCase := range small_windows {
			for _, level := range []int{flate.HuffmanOnly, flate.NoCompression} {
				var steps []string
				var sent []string
				di := base_di()
				di.conn.peerIsClient = testCase.peerIsClient
				di.conn.permessageDeflateAgreedConfig = &testCase.agreed
				di.conn.currentTransmitDataMsgDeflating = true
				want_window_bits := uint(11)
				di.sendWindowBits = func(agreed *PermessageDeflateConfig, peerIsClient bool) uint {
					steps = append(steps, fmt.Sprintf("sendWindowBits %+v %v", *agreed, peerIsClient))
					return want_window_bits
				}
				di.validateDeflateLevelForWindow = func(level int, window_bits uint) error {
					if window_bits != want_window_bits {
						t.Errorf(
							"%s: validateDeflateLevelForWindow got window_bits %d, want %d from sendWindowBits",
							testCase.name, window_bits, want_window_bits,
						)
					}
					steps = append(steps, fmt.Sprintf("validateDeflateLevelForWindow %d", level))
					return nil
				}
				di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func(data []byte, level int) ([]byte, error) {
					steps = append(steps, fmt.Sprintf("deflate %s %d", data, level))
					return append([]byte("z:"), append(data, 0x00, 0x00, 0xff, 0xff)...), nil
				}}
				di.sendFrame = func(f *Frame) error {
					sent = append(sent, fmt.Sprintf("%d %v %v %q", f.Opcode, f.FIN, f.RSV1, f.PayloadData))
					return nil
				}

				if err := transmitData([]byte("hello"), TransmitDataConfig{DeflateLevel: level}, di); err != nil {
					t.Fatalf("%s, level %d: transmitData: %v", testCase.name, level, err)
				}
				want_steps := []string{
					fmt.Sprintf("sendWindowBits %+v %v", testCase.agreed, testCase.peerIsClient),
					fmt.Sprintf("validateDeflateLevelForWindow %d", level),
					fmt.Sprintf("deflate hello %d", level),
				}
				if !slices.Equal(steps, want_steps) {
					t.Errorf("%s, level %d: steps %q, want %q", testCase.name, level, steps, want_steps)
				}
				if want := []string{`1 false true "z:hello\x00\x00\xff\xff"`}; !slices.Equal(sent, want) {
					t.Errorf("%s, level %d: sent %q, want %q", testCase.name, level, sent, want)
				}
			}
		}
	})

	// Nothing is deflated, so the window does not limit the level.
	t.Run("calls no sendWindowBits or validateDeflateLevelForWindow when the message is not deflated", func(t *testing.T) {
		for _, testCase := range small_windows {
			var sent []string
			di := base_di()
			di.conn.peerIsClient = testCase.peerIsClient
			di.conn.permessageDeflateAgreedConfig = &testCase.agreed
			di.sendWindowBits = func(*PermessageDeflateConfig, bool) uint {
				t.Errorf("%s: sendWindowBits called", testCase.name)
				return 9
			}
			di.validateDeflateLevelForWindow = func(int, uint) error {
				t.Errorf("%s: validateDeflateLevelForWindow called", testCase.name)
				return ErrDeflateLevelUnsupportedForWindow
			}
			di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
				t.Errorf("%s: deflated a message not deflated", testCase.name)
				return nil, nil
			}}
			di.sendFrame = func(f *Frame) error {
				sent = append(sent, fmt.Sprintf("%d %v %v %q", f.Opcode, f.FIN, f.RSV1, f.PayloadData))
				return nil
			}

			if err := transmitData([]byte("hello"), TransmitDataConfig{DeflateLevel: flate.BestCompression}, di); err != nil {
				t.Fatalf("%s: transmitData: %v", testCase.name, err)
			}
			if want := []string{`1 false false "hello"`}; !slices.Equal(sent, want) {
				t.Errorf("%s: sent %q, want %q", testCase.name, sent, want)
			}
		}
	})

	t.Run("sends empty data", func(t *testing.T) {
		var sent []*Frame
		di := base_di()
		di.sendFrame = func(f *Frame) error {
			sent = append(sent, f)
			return nil
		}

		if err := transmitData(nil, TransmitDataConfig{}, di); err != nil {
			t.Fatalf("transmitData: %v", err)
		}
		if len(sent) != 1 || sent[0].Opcode != OpcodeText || sent[0].Mask ||
			len(sent[0].PayloadData) != 0 || sent[0].FIN {
			t.Fatalf("sent %+v, want one unmasked empty frame opcode %#x no FIN", sent, OpcodeText)
		}
		if !di.conn.currentTransmitDataMsgOpened {
			t.Error("an empty fragment did not open the message")
		}
	})

	t.Run("propagates a build error", func(t *testing.T) {
		wantErr := errors.New("cannot build")
		di := base_di()
		di.conn.di.newDataFrame = func(NewFrameConfig) (*Frame, error) { return nil, wantErr }
		di.sendFrame = func(*Frame) error {
			t.Error("sent after a build failure")
			return nil
		}

		if err := transmitData([]byte("hello"), TransmitDataConfig{}, di); !errors.Is(err, wantErr) {
			t.Errorf("transmitData = %v, want %v", err, wantErr)
		}
	})

	// A fragment that never went out did not open the message.
	t.Run("propagates a send error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		di := base_di()
		di.sendFrame = func(*Frame) error { return wantErr }

		if err := transmitData([]byte("hello"), TransmitDataConfig{}, di); !errors.Is(err, wantErr) {
			t.Errorf("transmitData = %v, want %v", err, wantErr)
		}
		if di.conn.currentTransmitDataMsgOpened {
			t.Error("a failed fragment opened the message")
		}
	})

	// RFC 7692 6: RSV1 on the first frame only. 7.2.1: the 00 00 ff ff stays,
	// only End strips it. The chunks are one stream, so contextDeflater.
	t.Run("sends the deflated fragment", func(t *testing.T) {
		for _, opened := range []bool{false, true} {
			var deflated []string
			var sent []string
			di := base_di()
			di.conn.currentTransmitDataMsgDeflating = true
			di.conn.currentTransmitDataMsgOpened = opened
			di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func(data []byte, level int) ([]byte, error) {
				deflated = append(deflated, fmt.Sprintf("%s %d", data, level))
				return append([]byte("z:"), append(data, 0x00, 0x00, 0xff, 0xff)...), nil
			}}
			di.conn.noContextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
				t.Error("deflated without context")
				return nil, nil
			}}
			di.sendFrame = func(f *Frame) error {
				sent = append(sent, fmt.Sprintf("%d %v %v %q", f.Opcode, f.FIN, f.RSV1, f.PayloadData))
				return nil
			}

			if err := transmitData([]byte("hello"), TransmitDataConfig{DeflateLevel: 9}, di); err != nil {
				t.Fatalf("opened %v: transmitData: %v", opened, err)
			}
			if want := []string{"hello 9"}; !slices.Equal(deflated, want) {
				t.Errorf("opened %v: deflate got %q, want %q", opened, deflated, want)
			}
			want := []string{`1 false true "z:hello\x00\x00\xff\xff"`}
			if opened {
				want = []string{`0 false false "z:hello\x00\x00\xff\xff"`}
			}
			if !slices.Equal(sent, want) {
				t.Errorf("opened %v: sent %q, want %q", opened, sent, want)
			}
		}
	})

	t.Run("sends as is when the message is not deflated", func(t *testing.T) {
		var sent []*Frame
		di := base_di()
		di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
			t.Error("deflated a message not deflated")
			return nil, nil
		}}
		di.sendFrame = func(f *Frame) error {
			sent = append(sent, f)
			return nil
		}

		if err := transmitData([]byte("hello"), TransmitDataConfig{DeflateLevel: 9}, di); err != nil {
			t.Fatalf("transmitData: %v", err)
		}
		if len(sent) != 1 || string(sent[0].PayloadData) != "hello" || sent[0].RSV1 {
			t.Errorf("sent %+v, want one frame \"hello\" without RSV1", sent)
		}
	})

	// The write lock is not held while deflating, so control frames get out.
	t.Run("deflates before the write lock", func(t *testing.T) {
		var steps []string
		di := base_di()
		di.conn.currentTransmitDataMsgDeflating = true
		di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
			steps = append(steps, "deflate")
			return []byte{0x00, 0x00, 0xff, 0xff}, nil
		}}
		di.conn.di.writeLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "write lock") },
			unlock: func() { steps = append(steps, "write unlock") },
		}
		di.sendFrame = func(*Frame) error {
			steps = append(steps, "send")
			return nil
		}

		if err := transmitData([]byte("hello"), TransmitDataConfig{}, di); err != nil {
			t.Fatalf("transmitData: %v", err)
		}
		if want := []string{"deflate", "write lock", "send", "write unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	// A chunk that fails to build is never deflated: its bytes would enter the
	// context.
	t.Run("does not deflate when the frame fails to build", func(t *testing.T) {
		wantErr := errors.New("cannot build")
		di := base_di()
		di.conn.currentTransmitDataMsgDeflating = true
		di.conn.di.newDataFrame = func(NewFrameConfig) (*Frame, error) { return nil, wantErr }
		di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
			t.Error("deflated after a build error")
			return nil, nil
		}}

		if err := transmitData([]byte("hello"), TransmitDataConfig{}, di); !errors.Is(err, wantErr) {
			t.Errorf("transmitData = %v, want %v", err, wantErr)
		}
	})

	t.Run("returns a deflate error with nothing sent", func(t *testing.T) {
		wantErr := errors.New("cannot deflate")
		di := base_di()
		di.conn.currentTransmitDataMsgDeflating = true
		di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) { return nil, wantErr }}
		di.sendFrame = func(*Frame) error {
			t.Error("sent after a deflate error")
			return nil
		}

		if err := transmitData([]byte("hello"), TransmitDataConfig{}, di); !errors.Is(err, wantErr) {
			t.Errorf("transmitData = %v, want %v", err, wantErr)
		}
		if di.conn.currentTransmitDataMsgOpened {
			t.Error("a failed fragment opened the message")
		}
	})
}

func TestEndLongDataTransmission(t *testing.T) {
	// base_di has a binary transmission open on a real Conn, and a sendFrame
	// that succeeds; each case overrides what it looks at.
	base_di := func() endLongDataTransmissionDI {
		netConn := newFakeConn(nil)
		conn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), NewConnConfig{PeerIsClient: true})
		conn.currentTransmitDataMsgOpcode = OpcodeBinary
		return endLongDataTransmissionDI{
			conn:      conn,
			sendFrame: func(f *Frame) error { return nil },

			sendWindowBits:                func(*PermessageDeflateConfig, bool) uint { return 15 },
			validateDeflateLevelForWindow: func(int, uint) error { return nil },
		}
	}

	// FIN set, unmasked as the send masks, and the message's own opcode only
	// when nothing went before (5.4).
	t.Run("sends the last frame", func(t *testing.T) {
		for _, opened := range []bool{false, true} {
			var sent []*Frame
			di := base_di()
			di.conn.currentTransmitDataMsgOpened = opened
			di.sendFrame = func(f *Frame) error {
				sent = append(sent, f)
				return nil
			}

			if err := endLongDataTransmission([]byte("last"), TransmitDataConfig{}, di); err != nil {
				t.Fatalf("endLongDataTransmission: %v", err)
			}
			wantOpcode := uint8(OpcodeBinary)
			if opened {
				wantOpcode = OpcodeContinuation
			}
			if len(sent) != 1 || sent[0].Opcode != wantOpcode || sent[0].Mask ||
				string(sent[0].PayloadData) != "last" || !sent[0].FIN {
				t.Fatalf("opened %v: sent %+v, want one unmasked frame opcode %#x \"last\" FIN",
					opened, sent, wantOpcode)
			}
		}
	})

	// 5.2 allows a 0 payload length, so End sends one even with nothing before.
	t.Run("sends an empty last frame", func(t *testing.T) {
		var sent []*Frame
		di := base_di()
		di.sendFrame = func(f *Frame) error {
			sent = append(sent, f)
			return nil
		}

		if err := endLongDataTransmission(nil, TransmitDataConfig{}, di); err != nil {
			t.Fatalf("endLongDataTransmission: %v", err)
		}
		if len(sent) != 1 || sent[0].Opcode != OpcodeBinary || len(sent[0].PayloadData) != 0 || !sent[0].FIN {
			t.Fatalf("sent %+v, want one empty binary frame with FIN", sent)
		}
	})

	// Release frees the connection, not End.
	t.Run("sends under the write lock and keeps the data lock", func(t *testing.T) {
		var steps []string
		di := base_di()
		di.conn.di.writeLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "write lock") },
			unlock: func() { steps = append(steps, "write unlock") },
		}
		di.conn.di.dataFramesWriteLocker = fakeFuncLocker{
			lock:   func() { steps = append(steps, "data lock") },
			unlock: func() { steps = append(steps, "data unlock") },
		}
		di.sendFrame = func(*Frame) error {
			steps = append(steps, "send")
			return nil
		}

		if err := endLongDataTransmission(nil, TransmitDataConfig{}, di); err != nil {
			t.Fatalf("endLongDataTransmission: %v", err)
		}
		if want := []string{"write lock", "send", "write unlock"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
		if di.conn.currentTransmitDataMsgOpcode == 0 {
			t.Error("End released the transmission; Release does that")
		}
	})

	t.Run("refuses with no transmission open", func(t *testing.T) {
		di := base_di()
		di.conn.currentTransmitDataMsgOpcode = 0
		di.sendFrame = func(*Frame) error {
			t.Error("sent with no transmission open")
			return nil
		}

		if err := endLongDataTransmission(nil, TransmitDataConfig{}, di); !errors.Is(err, ErrLongDataTransmissionNotStarted) {
			t.Errorf("endLongDataTransmission = %v, want ErrLongDataTransmissionNotStarted", err)
		}
	})

	t.Run("refuses an invalid deflate level before anything runs", func(t *testing.T) {
		// Go's compress/flate has no level -3 or 10. It only has -2 to 9.
		for _, level := range []int{-3, 10} {
			for _, deflating := range []bool{false, true} {
				var steps []string
				di := base_di()
				di.conn.permessageDeflateAgreedConfig = &PermessageDeflateConfig{}
				di.conn.currentTransmitDataMsgDeflating = deflating
				di.conn.di.newDataFrame = func(NewFrameConfig) (*Frame, error) {
					steps = append(steps, "build")
					return &Frame{}, nil
				}
				di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
					steps = append(steps, "deflate")
					return []byte{0x00, 0x00, 0xff, 0xff}, nil
				}}
				di.sendWindowBits = func(*PermessageDeflateConfig, bool) uint {
					steps = append(steps, "sendWindowBits")
					return 15
				}
				di.validateDeflateLevelForWindow = func(int, uint) error {
					steps = append(steps, "validateDeflateLevelForWindow")
					return nil
				}
				di.sendFrame = func(*Frame) error {
					steps = append(steps, "send")
					return nil
				}

				if err := endLongDataTransmission(nil, TransmitDataConfig{DeflateLevel: level}, di); !errors.Is(err, ErrDeflateLevelInvalid) {
					t.Errorf("level %d, deflating %v: endLongDataTransmission = %v, want ErrDeflateLevelInvalid", level, deflating, err)
				}
				if len(steps) != 0 {
					t.Errorf("level %d, deflating %v: steps %q, want nothing run", level, deflating, steps)
				}
			}
		}
	})

	// RFC 7692 7.1.2: the window comes from this side's agreed config, and
	// the level is checked against it.
	small_windows := []struct {
		name         string
		agreed       PermessageDeflateConfig
		peerIsClient bool
	}{
		{
			name:         "server, server_max_window_bits=9",
			agreed:       PermessageDeflateConfig{ServerMaxWindowBits: 9},
			peerIsClient: true,
		},
		{
			name:         "client, client_max_window_bits=9",
			agreed:       PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 9},
			peerIsClient: false,
		},
	}

	t.Run("returns the validateDeflateLevelForWindow error and sends nothing", func(t *testing.T) {
		for _, testCase := range small_windows {
			var steps []string
			di := base_di()
			di.conn.peerIsClient = testCase.peerIsClient
			di.conn.permessageDeflateAgreedConfig = &testCase.agreed
			di.conn.currentTransmitDataMsgDeflating = true
			want_window_bits := uint(11)
			validate_deflate_level_for_window_error := errors.New("validateDeflateLevelForWindow error")
			di.sendWindowBits = func(agreed *PermessageDeflateConfig, peerIsClient bool) uint {
				steps = append(steps, fmt.Sprintf("sendWindowBits %+v %v", *agreed, peerIsClient))
				return want_window_bits
			}
			di.validateDeflateLevelForWindow = func(level int, window_bits uint) error {
				if window_bits != want_window_bits {
					t.Errorf(
						"%s: validateDeflateLevelForWindow got window_bits %d, want %d from sendWindowBits",
						testCase.name, window_bits, want_window_bits,
					)
				}
				steps = append(steps, fmt.Sprintf("validateDeflateLevelForWindow %d", level))
				return validate_deflate_level_for_window_error
			}
			di.conn.di.newDataFrame = func(NewFrameConfig) (*Frame, error) {
				steps = append(steps, "build")
				return &Frame{}, nil
			}
			di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
				steps = append(steps, "deflate")
				return []byte{0x00, 0x00, 0xff, 0xff}, nil
			}}
			di.sendFrame = func(*Frame) error {
				steps = append(steps, "send")
				return nil
			}

			config := TransmitDataConfig{DeflateLevel: flate.DefaultCompression}
			if err := endLongDataTransmission([]byte("hello"), config, di); !errors.Is(err, validate_deflate_level_for_window_error) {
				t.Errorf("%s: endLongDataTransmission = %v, want the validateDeflateLevelForWindow error", testCase.name, err)
			}
			want := []string{fmt.Sprintf("sendWindowBits %+v %v", testCase.agreed, testCase.peerIsClient), "validateDeflateLevelForWindow -1"}
			if !slices.Equal(steps, want) {
				t.Errorf("%s: steps %q, want %q", testCase.name, steps, want)
			}
			if !di.conn.currentTransmitDataMsgDeflating {
				t.Errorf("%s: a refused last frame ended the deflated stream", testCase.name)
			}
		}
	})

	t.Run("deflates and sends when validateDeflateLevelForWindow returns nil", func(t *testing.T) {
		for _, testCase := range small_windows {
			for _, level := range []int{flate.HuffmanOnly, flate.NoCompression} {
				var steps []string
				var sent []string
				di := base_di()
				di.conn.peerIsClient = testCase.peerIsClient
				di.conn.permessageDeflateAgreedConfig = &testCase.agreed
				di.conn.currentTransmitDataMsgDeflating = true
				want_window_bits := uint(11)
				di.sendWindowBits = func(agreed *PermessageDeflateConfig, peerIsClient bool) uint {
					steps = append(steps, fmt.Sprintf("sendWindowBits %+v %v", *agreed, peerIsClient))
					return want_window_bits
				}
				di.validateDeflateLevelForWindow = func(level int, window_bits uint) error {
					if window_bits != want_window_bits {
						t.Errorf(
							"%s: validateDeflateLevelForWindow got window_bits %d, want %d from sendWindowBits",
							testCase.name, window_bits, want_window_bits,
						)
					}
					steps = append(steps, fmt.Sprintf("validateDeflateLevelForWindow %d", level))
					return nil
				}
				di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func(data []byte, level int) ([]byte, error) {
					steps = append(steps, fmt.Sprintf("deflate %s %d", data, level))
					return append([]byte("z:"), append(data, 0x00, 0x00, 0xff, 0xff)...), nil
				}}
				di.sendFrame = func(f *Frame) error {
					sent = append(sent, fmt.Sprintf("%d %v %v %q", f.Opcode, f.FIN, f.RSV1, f.PayloadData))
					return nil
				}

				if err := endLongDataTransmission([]byte("hello"), TransmitDataConfig{DeflateLevel: level}, di); err != nil {
					t.Fatalf("%s, level %d: endLongDataTransmission: %v", testCase.name, level, err)
				}
				want_steps := []string{
					fmt.Sprintf("sendWindowBits %+v %v", testCase.agreed, testCase.peerIsClient),
					fmt.Sprintf("validateDeflateLevelForWindow %d", level),
					fmt.Sprintf("deflate hello %d", level),
				}
				if !slices.Equal(steps, want_steps) {
					t.Errorf("%s, level %d: steps %q, want %q", testCase.name, level, steps, want_steps)
				}
				if want := []string{`2 true true "z:hello"`}; !slices.Equal(sent, want) {
					t.Errorf("%s, level %d: sent %q, want %q", testCase.name, level, sent, want)
				}
			}
		}
	})

	// Nothing is deflated, so the window does not limit the level.
	t.Run("calls no sendWindowBits or validateDeflateLevelForWindow when the message is not deflated", func(t *testing.T) {
		for _, testCase := range small_windows {
			var sent []string
			di := base_di()
			di.conn.peerIsClient = testCase.peerIsClient
			di.conn.permessageDeflateAgreedConfig = &testCase.agreed
			di.sendWindowBits = func(*PermessageDeflateConfig, bool) uint {
				t.Errorf("%s: sendWindowBits called", testCase.name)
				return 9
			}
			di.validateDeflateLevelForWindow = func(int, uint) error {
				t.Errorf("%s: validateDeflateLevelForWindow called", testCase.name)
				return ErrDeflateLevelUnsupportedForWindow
			}
			di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
				t.Errorf("%s: deflated a message not deflated", testCase.name)
				return nil, nil
			}}
			di.sendFrame = func(f *Frame) error {
				sent = append(sent, fmt.Sprintf("%d %v %v %q", f.Opcode, f.FIN, f.RSV1, f.PayloadData))
				return nil
			}

			if err := endLongDataTransmission([]byte("hello"), TransmitDataConfig{DeflateLevel: flate.BestCompression}, di); err != nil {
				t.Fatalf("%s: endLongDataTransmission: %v", testCase.name, err)
			}
			if want := []string{`2 true false "hello"`}; !slices.Equal(sent, want) {
				t.Errorf("%s: sent %q, want %q", testCase.name, sent, want)
			}
		}
	})

	t.Run("propagates a build error", func(t *testing.T) {
		wantErr := errors.New("cannot build")
		di := base_di()
		di.conn.di.newDataFrame = func(NewFrameConfig) (*Frame, error) { return nil, wantErr }
		di.sendFrame = func(*Frame) error {
			t.Error("sent after a build failure")
			return nil
		}

		if err := endLongDataTransmission(nil, TransmitDataConfig{}, di); !errors.Is(err, wantErr) {
			t.Errorf("endLongDataTransmission = %v, want %v", err, wantErr)
		}
	})

	t.Run("propagates a send error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		di := base_di()
		di.sendFrame = func(*Frame) error { return wantErr }

		if err := endLongDataTransmission(nil, TransmitDataConfig{}, di); !errors.Is(err, wantErr) {
			t.Errorf("endLongDataTransmission = %v, want %v", err, wantErr)
		}
	})

	// RFC 7692 6: RSV1 on the first frame only. 7.2.1: the trailing
	// 00 00 ff ff is not sent. The stream is whole, so nothing is left for
	// Release to drop.
	t.Run("sends the deflated last frame", func(t *testing.T) {
		for _, opened := range []bool{false, true} {
			var deflated []string
			var sent []string
			di := base_di()
			di.conn.permessageDeflateAgreedConfig = &PermessageDeflateConfig{}
			di.conn.currentTransmitDataMsgDeflating = true
			di.conn.currentTransmitDataMsgOpened = opened
			di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func(data []byte, level int) ([]byte, error) {
				deflated = append(deflated, fmt.Sprintf("%s %d", data, level))
				return append([]byte("z:"), append(data, 0x00, 0x00, 0xff, 0xff)...), nil
			}}
			di.sendFrame = func(f *Frame) error {
				sent = append(sent, fmt.Sprintf("%d %v %v %s", f.Opcode, f.FIN, f.RSV1, f.PayloadData))
				return nil
			}

			if err := endLongDataTransmission([]byte("last"), TransmitDataConfig{DeflateLevel: 9}, di); err != nil {
				t.Fatalf("opened %v: endLongDataTransmission: %v", opened, err)
			}
			if want := []string{"last 9"}; !slices.Equal(deflated, want) {
				t.Errorf("opened %v: deflate got %q, want %q", opened, deflated, want)
			}
			want := []string{"2 true true z:last"}
			if opened {
				want = []string{"0 true false z:last"}
			}
			if !slices.Equal(sent, want) {
				t.Errorf("opened %v: sent %q, want %q", opened, sent, want)
			}
			if di.conn.currentTransmitDataMsgDeflating {
				t.Errorf("opened %v: the stream is still open after End", opened)
			}
		}
	})

	t.Run("sends as is when the message is not deflated", func(t *testing.T) {
		var sent []*Frame
		di := base_di()
		di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
			t.Error("deflated a message not deflated")
			return nil, nil
		}}
		di.sendFrame = func(f *Frame) error {
			sent = append(sent, f)
			return nil
		}

		if err := endLongDataTransmission([]byte("last"), TransmitDataConfig{DeflateLevel: 9}, di); err != nil {
			t.Fatalf("endLongDataTransmission: %v", err)
		}
		if len(sent) != 1 || string(sent[0].PayloadData) != "last" || sent[0].RSV1 {
			t.Errorf("sent %+v, want one frame \"last\" without RSV1", sent)
		}
	})

	/*
		RFC 7692 7.1.1: server_no_context_takeover binds a server,
		client_no_context_takeover a client. A side bound by it drops the
		context once the message is deflated, before the send, so a failed send
		drops it too.
	*/
	t.Run("drops the context by this side's context takeover", func(t *testing.T) {
		for _, testCase := range []struct {
			name         string
			peerIsClient bool
			agreed       PermessageDeflateConfig
			want         []string
		}{
			{
				name:         "a server with server_no_context_takeover",
				peerIsClient: true,
				agreed:       PermessageDeflateConfig{ServerNoContextTakeover: true},
				want:         []string{"deflate", "drop", "write lock", "send", "write unlock"},
			},
			{
				name:         "a server with client_no_context_takeover only",
				peerIsClient: true,
				agreed:       PermessageDeflateConfig{ClientNoContextTakeover: true},
				want:         []string{"deflate", "write lock", "send", "write unlock"},
			},
			{
				name:         "a client with client_no_context_takeover",
				peerIsClient: false,
				agreed:       PermessageDeflateConfig{ClientNoContextTakeover: true},
				want:         []string{"deflate", "drop", "write lock", "send", "write unlock"},
			},
			{
				name:         "a client with server_no_context_takeover only",
				peerIsClient: false,
				agreed:       PermessageDeflateConfig{ServerNoContextTakeover: true},
				want:         []string{"deflate", "write lock", "send", "write unlock"},
			},
			{
				name:         "a server with neither",
				peerIsClient: true,
				agreed:       PermessageDeflateConfig{},
				want:         []string{"deflate", "write lock", "send", "write unlock"},
			},
			{
				name:         "a client with neither",
				peerIsClient: false,
				agreed:       PermessageDeflateConfig{},
				want:         []string{"deflate", "write lock", "send", "write unlock"},
			},
		} {
			var steps []string
			di := base_di()
			di.conn.peerIsClient = testCase.peerIsClient
			di.conn.permessageDeflateAgreedConfig = &testCase.agreed
			di.conn.currentTransmitDataMsgDeflating = true
			di.conn.contextDeflater = fakeFuncDeflater{
				deflateFunc: func([]byte, int) ([]byte, error) {
					steps = append(steps, "deflate")
					return []byte{0x00, 0x00, 0xff, 0xff}, nil
				},
				dropContextFunc: func() { steps = append(steps, "drop") },
			}
			di.conn.di.writeLocker = fakeFuncLocker{
				lock:   func() { steps = append(steps, "write lock") },
				unlock: func() { steps = append(steps, "write unlock") },
			}
			di.sendFrame = func(*Frame) error {
				steps = append(steps, "send")
				return nil
			}

			if err := endLongDataTransmission(nil, TransmitDataConfig{}, di); err != nil {
				t.Fatalf("%s: endLongDataTransmission: %v", testCase.name, err)
			}
			if !slices.Equal(steps, testCase.want) {
				t.Errorf("%s: steps %q, want %q", testCase.name, steps, testCase.want)
			}
		}
	})

	// A chunk that fails to build is never deflated: its bytes would enter the
	// context.
	t.Run("does not deflate when the frame fails to build", func(t *testing.T) {
		wantErr := errors.New("cannot build")
		di := base_di()
		di.conn.permessageDeflateAgreedConfig = &PermessageDeflateConfig{}
		di.conn.currentTransmitDataMsgDeflating = true
		di.conn.di.newDataFrame = func(NewFrameConfig) (*Frame, error) { return nil, wantErr }
		di.conn.contextDeflater = fakeFuncDeflater{deflateFunc: func([]byte, int) ([]byte, error) {
			t.Error("deflated after a build error")
			return nil, nil
		}}

		if err := endLongDataTransmission(nil, TransmitDataConfig{}, di); !errors.Is(err, wantErr) {
			t.Errorf("endLongDataTransmission = %v, want %v", err, wantErr)
		}
	})

	// The stream is not whole, so Release is left to drop the context.
	t.Run("returns a deflate error with nothing sent", func(t *testing.T) {
		wantErr := errors.New("cannot deflate")
		di := base_di()
		di.conn.permessageDeflateAgreedConfig = &PermessageDeflateConfig{ServerNoContextTakeover: true}
		di.conn.currentTransmitDataMsgDeflating = true
		di.conn.contextDeflater = fakeFuncDeflater{
			deflateFunc: func([]byte, int) ([]byte, error) { return nil, wantErr },
			dropContextFunc: func() {
				t.Error("dropped the context after a deflate error")
			},
		}
		di.sendFrame = func(*Frame) error {
			t.Error("sent after a deflate error")
			return nil
		}

		if err := endLongDataTransmission(nil, TransmitDataConfig{}, di); !errors.Is(err, wantErr) {
			t.Errorf("endLongDataTransmission = %v, want %v", err, wantErr)
		}
		if !di.conn.currentTransmitDataMsgDeflating {
			t.Error("a failed End closed the stream")
		}
	})
}

func TestConnReleaseLongDataTransmission(t *testing.T) {
	// Every field, or the next transmission inherits this one: a stale opened
	// would send its first frame as a continuation. The unlock comes last, so
	// the next transmission never sees them before they are reset.
	t.Run("resets the transmission, then unlocks", func(t *testing.T) {
		var steps []string
		netConn := newFakeConn(nil)
		conn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), NewConnConfig{PeerIsClient: true})
		conn.contextDeflater = fakeFuncDeflater{dropContextFunc: func() { steps = append(steps, "drop") }}
		conn.di.dataFramesWriteLocker = fakeFuncLocker{
			lock: func() { steps = append(steps, "data lock") },
			unlock: func() {
				steps = append(steps, fmt.Sprintf("data unlock opcode=%#x opened=%v deflating=%v",
					conn.currentTransmitDataMsgOpcode, conn.currentTransmitDataMsgOpened, conn.currentTransmitDataMsgDeflating))
			},
		}
		conn.currentTransmitDataMsgOpcode = OpcodeText
		conn.currentTransmitDataMsgOpened = true

		conn.ReleaseLongDataTransmission()

		if want := []string{"data unlock opcode=0x0 opened=false deflating=false"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})

	// A deflated message released before End leaves chunks in the context
	// the peer may not have.
	t.Run("drops the context of a deflated message not ended", func(t *testing.T) {
		var steps []string
		netConn := newFakeConn(nil)
		conn, _ := NewConn(netConn, bufio.NewReader(netConn), bufio.NewWriter(netConn), NewConnConfig{PeerIsClient: true})
		conn.contextDeflater = fakeFuncDeflater{dropContextFunc: func() { steps = append(steps, "drop") }}
		conn.di.dataFramesWriteLocker = fakeFuncLocker{
			lock: func() { steps = append(steps, "data lock") },
			unlock: func() {
				steps = append(steps, fmt.Sprintf("data unlock deflating=%v", conn.currentTransmitDataMsgDeflating))
			},
		}
		conn.currentTransmitDataMsgOpcode = OpcodeText
		conn.currentTransmitDataMsgDeflating = true

		conn.ReleaseLongDataTransmission()

		if want := []string{"drop", "data unlock deflating=false"}; !slices.Equal(steps, want) {
			t.Errorf("steps %q, want %q", steps, want)
		}
	})
}
