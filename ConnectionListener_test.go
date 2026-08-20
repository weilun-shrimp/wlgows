package wlgows

import (
	"bufio"
	"bytes"
	"testing"
)

// hookConn records what a hook builds, so a test reads the answer without
// parsing the wire. The frame still goes out through the real SendFrame, which
// is what sets closeSent.
func hookConn() (*Conn, *[]NewControlFrameConfig) {
	var sent []NewControlFrameConfig
	conn := NewConn(newFakeConn(nil), bufio.NewReader(newFakeConn(nil)), false)
	conn.di.newControlFrame = func(config NewControlFrameConfig) (*Frame, error) {
		sent = append(sent, config)
		return NewControlFrame(config)
	}
	return conn, &sent
}

// 5.5.2: MUST answer with a pong whose payload is identical.
func TestConnRFC6455PingHook(t *testing.T) {
	conn, sent := hookConn()

	conn.RFC6455PingHook(&Frame{Opcode: OpcodePing, FIN: true, PayloadData: []byte("hb")})

	if len(*sent) != 1 {
		t.Fatalf("built %d frames, want 1", len(*sent))
	}
	if (*sent)[0].Opcode != OpcodePong {
		t.Errorf("opcode = %#x, want OpcodePong", (*sent)[0].Opcode)
	}
	if !bytes.Equal((*sent)[0].PayloadData, []byte("hb")) {
		t.Errorf("payload = %q, want hb", (*sent)[0].PayloadData)
	}
}

// 5.5.3: a pong MUST NOT be answered.
func TestConnRFC6455PongHook(t *testing.T) {
	conn, sent := hookConn()

	conn.RFC6455PongHook(&Frame{Opcode: OpcodePong, FIN: true, PayloadData: []byte("hb")})

	if len(*sent) != 0 {
		t.Errorf("built %d frames, want nothing on the wire", len(*sent))
	}
}

// 5.5.1: answer with a close echoing the status code, then read nothing further.
func TestConnRFC6455CloseHook(t *testing.T) {
	conn, sent := hookConn()
	l := NewListener(conn)
	l.pauseChan = make(chan error, 1) // a run to release
	body := (&ClosePayload{StatusCode: CloseNormalClosure, Reason: "bye"}).Bytes()

	conn.RFC6455CloseHook(l)(&Frame{Opcode: OpcodeClose, FIN: true, PayloadData: body})

	if len(*sent) != 1 {
		t.Fatalf("built %d frames, want 1", len(*sent))
	}
	if (*sent)[0].Opcode != OpcodeClose {
		t.Errorf("opcode = %#x, want OpcodeClose", (*sent)[0].Opcode)
	}
	if !bytes.Equal((*sent)[0].PayloadData, body) {
		t.Errorf("payload = % x, want % x — the status code is echoed", (*sent)[0].PayloadData, body)
	}
	if l.pauseChan != nil {
		t.Error("the run was not paused")
	}

	// One close each way: this side has now sent one, so the peer's answer gets
	// no second close out of us.
	conn.RFC6455CloseHook(l)(&Frame{Opcode: OpcodeClose, FIN: true})

	if len(*sent) != 1 {
		t.Errorf("built %d close frames, want 1", len(*sent))
	}
}

// 5.2 reserves the opcode, which 7.4.1 makes a protocol error: 1002.
func TestConnRFC6455UnknownHook(t *testing.T) {
	conn, sent := hookConn()
	l := NewListener(conn)
	l.pauseChan = make(chan error, 1)

	conn.RFC6455UnknownHook(l)(&Frame{Opcode: 0xB, FIN: true})

	if len(*sent) != 1 {
		t.Fatalf("built %d frames, want 1", len(*sent))
	}
	want := (&ClosePayload{StatusCode: CloseProtocolError}).Bytes()
	if (*sent)[0].Opcode != OpcodeClose || !bytes.Equal((*sent)[0].PayloadData, want) {
		t.Errorf("built %+v, want a close carrying 1002", (*sent)[0])
	}
	if l.pauseChan != nil {
		t.Error("the run was not paused")
	}
}

/*
NewStandardListener wires the four hooks and the masking direction. A ping is
routed through to catch the mistake nil checks cannot: a hook on the wrong slot.
*/
func TestConnNewStandardListener(t *testing.T) {
	conn, sent := hookConn()

	l := conn.NewStandardListener()

	// 5.1: this Conn does not mask, so it is a server and the peer is a client.
	if !l.config.PeerIsClient {
		t.Error("PeerIsClient = false, want true for a server Conn")
	}
	if l.config.Ping == nil || l.config.Pong == nil || l.config.Close == nil || l.config.Unknown == nil {
		t.Error("a protocol hook was left nil")
	}
	// The message hooks are the caller's half.
	if l.config.Text != nil || l.config.Binary != nil || l.config.Data != nil {
		t.Error("a message hook was set")
	}

	l.routeFrame(&Frame{Opcode: OpcodePing, FIN: true, PayloadData: []byte("hb")})

	if len(*sent) != 1 || (*sent)[0].Opcode != OpcodePong {
		t.Fatalf("built %d frames, want one pong", len(*sent))
	}
}
