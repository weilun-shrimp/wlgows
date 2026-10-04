package wlgows

import (
	"bytes"
	"errors"
	"testing"
)

func TestSendClose(t *testing.T) {
	// base_di builds and sends successfully; each case overrides what it looks at.
	base_di := func() sendCloseDI {
		return sendCloseDI{
			newControlFrame: func(config NewControlFrameConfig) (*Frame, error) {
				return &Frame{Opcode: config.Opcode, PayloadData: config.PayloadData, FIN: true}, nil
			},
			SendFrame: func(f *Frame) error { return nil },
		}
	}

	t.Run("builds a close from the payload and sends it", func(t *testing.T) {
		payload := &ClosePayload{StatusCode: CloseGoingAway, Reason: "restart"}
		var configs []NewControlFrameConfig
		built := &Frame{}
		var sent []*Frame
		di := base_di()
		di.newControlFrame = func(config NewControlFrameConfig) (*Frame, error) {
			configs = append(configs, config)
			return built, nil
		}
		di.SendFrame = func(f *Frame) error {
			sent = append(sent, f)
			return nil
		}

		if err := sendClose(payload, di); err != nil {
			t.Fatalf("sendClose: %v", err)
		}
		if len(configs) != 1 || configs[0].Opcode != OpcodeClose || !bytes.Equal(configs[0].PayloadData, payload.Bytes()) {
			t.Errorf("configs %+v, want one {Opcode: close, PayloadData: % x}", configs, payload.Bytes())
		}
		if len(sent) != 1 || sent[0] != built {
			t.Errorf("sent %d frames, want the built one once", len(sent))
		}
	})

	// 7.1.5: a close may carry no body at all.
	t.Run("a nil payload builds a close with no body", func(t *testing.T) {
		var configs []NewControlFrameConfig
		di := base_di()
		di.newControlFrame = func(config NewControlFrameConfig) (*Frame, error) {
			configs = append(configs, config)
			return &Frame{}, nil
		}

		if err := sendClose(nil, di); err != nil {
			t.Fatalf("sendClose: %v", err)
		}
		if len(configs) != 1 || configs[0].Opcode != OpcodeClose || configs[0].PayloadData != nil {
			t.Errorf("configs %+v, want one {Opcode: close, PayloadData: nil}", configs)
		}
	})

	t.Run("returns a build error with nothing sent", func(t *testing.T) {
		wantErr := errors.New("cannot build")
		di := base_di()
		di.newControlFrame = func(NewControlFrameConfig) (*Frame, error) { return nil, wantErr }
		di.SendFrame = func(*Frame) error {
			t.Error("sent after a build error")
			return nil
		}

		if err := sendClose(nil, di); !errors.Is(err, wantErr) {
			t.Errorf("sendClose = %v, want %v", err, wantErr)
		}
	})

	t.Run("returns the send error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		di := base_di()
		di.SendFrame = func(*Frame) error { return wantErr }

		if err := sendClose(nil, di); !errors.Is(err, wantErr) {
			t.Errorf("sendClose = %v, want %v", err, wantErr)
		}
	})
}

func TestSendPing(t *testing.T) {
	// base_di builds and sends successfully; each case overrides what it looks at.
	base_di := func() sendPingDI {
		return sendPingDI{
			newControlFrame: func(config NewControlFrameConfig) (*Frame, error) {
				return &Frame{Opcode: config.Opcode, PayloadData: config.PayloadData, FIN: true}, nil
			},
			SendFrame: func(f *Frame) error { return nil },
		}
	}

	// 5.5.2: the pong must echo this payload, so it goes in as given.
	t.Run("builds a ping from the payload and sends it", func(t *testing.T) {
		payloadData := []byte("hb")
		var configs []NewControlFrameConfig
		built := &Frame{}
		var sent []*Frame
		di := base_di()
		di.newControlFrame = func(config NewControlFrameConfig) (*Frame, error) {
			configs = append(configs, config)
			return built, nil
		}
		di.SendFrame = func(f *Frame) error {
			sent = append(sent, f)
			return nil
		}

		if err := sendPing(payloadData, di); err != nil {
			t.Fatalf("sendPing: %v", err)
		}
		if len(configs) != 1 || configs[0].Opcode != OpcodePing || !bytes.Equal(configs[0].PayloadData, payloadData) {
			t.Errorf("configs %+v, want one {Opcode: ping, PayloadData: hb}", configs)
		}
		if len(sent) != 1 || sent[0] != built {
			t.Errorf("sent %d frames, want the built one once", len(sent))
		}
	})

	t.Run("returns a build error with nothing sent", func(t *testing.T) {
		wantErr := errors.New("cannot build")
		di := base_di()
		di.newControlFrame = func(NewControlFrameConfig) (*Frame, error) { return nil, wantErr }
		di.SendFrame = func(*Frame) error {
			t.Error("sent after a build error")
			return nil
		}

		if err := sendPing(nil, di); !errors.Is(err, wantErr) {
			t.Errorf("sendPing = %v, want %v", err, wantErr)
		}
	})

	t.Run("returns the send error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		di := base_di()
		di.SendFrame = func(*Frame) error { return wantErr }

		if err := sendPing(nil, di); !errors.Is(err, wantErr) {
			t.Errorf("sendPing = %v, want %v", err, wantErr)
		}
	})
}

func TestSendPong(t *testing.T) {
	// base_di builds and sends successfully; each case overrides what it looks at.
	base_di := func() sendPongDI {
		return sendPongDI{
			newControlFrame: func(config NewControlFrameConfig) (*Frame, error) {
				return &Frame{Opcode: config.Opcode, PayloadData: config.PayloadData, FIN: true}, nil
			},
			SendFrame: func(f *Frame) error { return nil },
		}
	}

	// 5.5.3: an answer echoes the ping's payload, so it goes in as given.
	t.Run("builds a pong from the payload and sends it", func(t *testing.T) {
		payloadData := []byte("hb")
		var configs []NewControlFrameConfig
		built := &Frame{}
		var sent []*Frame
		di := base_di()
		di.newControlFrame = func(config NewControlFrameConfig) (*Frame, error) {
			configs = append(configs, config)
			return built, nil
		}
		di.SendFrame = func(f *Frame) error {
			sent = append(sent, f)
			return nil
		}

		if err := sendPong(payloadData, di); err != nil {
			t.Fatalf("sendPong: %v", err)
		}
		if len(configs) != 1 || configs[0].Opcode != OpcodePong || !bytes.Equal(configs[0].PayloadData, payloadData) {
			t.Errorf("configs %+v, want one {Opcode: pong, PayloadData: hb}", configs)
		}
		if len(sent) != 1 || sent[0] != built {
			t.Errorf("sent %d frames, want the built one once", len(sent))
		}
	})

	t.Run("returns a build error with nothing sent", func(t *testing.T) {
		wantErr := errors.New("cannot build")
		di := base_di()
		di.newControlFrame = func(NewControlFrameConfig) (*Frame, error) { return nil, wantErr }
		di.SendFrame = func(*Frame) error {
			t.Error("sent after a build error")
			return nil
		}

		if err := sendPong(nil, di); !errors.Is(err, wantErr) {
			t.Errorf("sendPong = %v, want %v", err, wantErr)
		}
	})

	t.Run("returns the send error", func(t *testing.T) {
		wantErr := errors.New("socket gone")
		di := base_di()
		di.SendFrame = func(*Frame) error { return wantErr }

		if err := sendPong(nil, di); !errors.Is(err, wantErr) {
			t.Errorf("sendPong = %v, want %v", err, wantErr)
		}
	})
}
