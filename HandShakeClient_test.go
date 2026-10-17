package wlgows

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestClientHandShake(t *testing.T) {
	// order records which steps ran, so short-circuiting is observable.
	newOrderTrackingDI := func(order *[]string) clientHandShakeDI {
		return clientHandShakeDI{
			upgradeRequest: func(*http.Request, ClientHandShakeConfig) error {
				*order = append(*order, "upgrade")
				return nil
			},
			sendHandShakeRequest: func(io.Writer, *http.Request) error {
				*order = append(*order, "send")
				return nil
			},
			readHandShakeResponse: func(*bufio.Reader, *http.Request) (*http.Response, error) {
				*order = append(*order, "read")
				return &http.Response{StatusCode: 101}, nil
			},
			validateHandShakeResponse: func(*http.Response, string) error {
				*order = append(*order, "validate")
				return nil
			},
			validateHandShakeResponseSubprotocol: func(*http.Response, *http.Request) error {
				*order = append(*order, "validateSubprotocol")
				return nil
			},
			validateHandShakeResponseDeflate: func(*http.Response, *http.Request) (*PermessageDeflateConfig, error) {
				*order = append(*order, "validateDeflate")
				return nil, nil
			},
			newConn: func(net.Conn, *bufio.Reader, *bufio.Writer, NewConnConfig) (*Conn, error) {
				*order = append(*order, "newConn")
				return &Conn{}, nil
			},
		}
	}

	t.Run("runs all seven steps in order", func(t *testing.T) {
		var order []string
		req := httptestRequest(t)
		if _, _, err := clientHandShake(nil, nil, nil, req, ClientHandShakeConfig{}, newOrderTrackingDI(&order)); err != nil {
			t.Fatalf("clientHandShake: %v", err)
		}
		want := []string{"upgrade", "send", "read", "validate", "validateSubprotocol", "validateDeflate", "newConn"}
		if strings.Join(order, ",") != strings.Join(want, ",") {
			t.Errorf("order = %v, want %v", order, want)
		}
	})

	t.Run("passes the Sec-WebSocket-Key to validation", func(t *testing.T) {
		var order []string
		di := newOrderTrackingDI(&order)
		req := httptestRequest(t)
		req.Header.Set("Sec-WebSocket-Key", "the-key")
		var gotKey string
		di.validateHandShakeResponse = func(_ *http.Response, key string) error {
			gotKey = key
			return nil
		}
		if _, _, err := clientHandShake(nil, nil, nil, req, ClientHandShakeConfig{}, di); err != nil {
			t.Fatalf("clientHandShake: %v", err)
		}
		if gotKey != "the-key" {
			t.Errorf("key = %q, want %q", gotKey, "the-key")
		}
	})

	t.Run("passes the config to upgradeRequest", func(t *testing.T) {
		var order []string
		di := newOrderTrackingDI(&order)
		want := ClientHandShakeConfig{
			Subprotocols:       []string{"chat", "superchat"},
			PermessageDeflates: []PermessageDeflateConfig{{}},
		}
		var got ClientHandShakeConfig
		di.upgradeRequest = func(_ *http.Request, config ClientHandShakeConfig) error {
			got = config
			return nil
		}
		if _, _, err := clientHandShake(nil, nil, nil, httptestRequest(t), want, di); err != nil {
			t.Fatalf("clientHandShake: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("config = %+v, want %+v", got, want)
		}
	})

	t.Run("returns the response even when validation fails", func(t *testing.T) {
		var order []string
		di := newOrderTrackingDI(&order)
		want := errors.New("bad accept")
		di.validateHandShakeResponse = func(*http.Response, string) error { return want }
		_, res, err := clientHandShake(nil, nil, nil, httptestRequest(t), ClientHandShakeConfig{}, di)
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
		if res == nil || res.StatusCode != 101 {
			t.Errorf("res = %v, want the parsed response to survive a validation failure", res)
		}
	})

	t.Run("passes the response and the request to the subprotocol check", func(t *testing.T) {
		var order []string
		di := newOrderTrackingDI(&order)
		request := httptestRequest(t)
		wantResp := &http.Response{StatusCode: 101}
		di.readHandShakeResponse = func(*bufio.Reader, *http.Request) (*http.Response, error) { return wantResp, nil }
		var gotResp *http.Response
		var gotRequest *http.Request
		di.validateHandShakeResponseSubprotocol = func(givenResp *http.Response, givenRequest *http.Request) error {
			gotResp, gotRequest = givenResp, givenRequest
			return nil
		}
		if _, _, err := clientHandShake(nil, nil, nil, request, ClientHandShakeConfig{}, di); err != nil {
			t.Fatalf("clientHandShake: %v", err)
		}
		if gotResp != wantResp || gotRequest != request {
			t.Error("the subprotocol check did not get the response read and the request sent")
		}
	})

	t.Run("sends the request on the net.Conn, not the bufio.Writer", func(t *testing.T) {
		var order []string
		di := newOrderTrackingDI(&order)
		netConn := newFakeConn(nil)
		var gotWriter io.Writer
		di.sendHandShakeRequest = func(givenWriter io.Writer, _ *http.Request) error {
			gotWriter = givenWriter
			return nil
		}
		if _, _, err := clientHandShake(netConn, nil, bufio.NewWriter(netConn), httptestRequest(t), ClientHandShakeConfig{}, di); err != nil {
			t.Fatalf("clientHandShake: %v", err)
		}
		if gotWriter != io.Writer(netConn) {
			t.Errorf("sendHandShakeRequest wrote to %v, want the net.Conn", gotWriter)
		}
	})

	t.Run("passes the net.Conn, bufio.Reader and bufio.Writer to newConn with masking and returns its Conn", func(t *testing.T) {
		var order []string
		di := newOrderTrackingDI(&order)
		netConn := newFakeConn(nil)
		reader := bufio.NewReader(netConn)
		writer := bufio.NewWriter(netConn)
		want := &Conn{}
		agreed := &PermessageDeflateConfig{ServerNoContextTakeover: true}
		di.validateHandShakeResponseDeflate = func(*http.Response, *http.Request) (*PermessageDeflateConfig, error) {
			return agreed, nil
		}
		var gotNetConn net.Conn
		var gotReader *bufio.Reader
		var gotWriter *bufio.Writer
		var gotConfig NewConnConfig
		di.newConn = func(givenNetConn net.Conn, givenReader *bufio.Reader, givenWriter *bufio.Writer, config NewConnConfig) (*Conn, error) {
			gotNetConn, gotReader, gotWriter, gotConfig = givenNetConn, givenReader, givenWriter, config
			return want, nil
		}
		conn, _, err := clientHandShake(netConn, reader, writer, httptestRequest(t), ClientHandShakeConfig{}, di)
		if err != nil {
			t.Fatalf("clientHandShake: %v", err)
		}
		if conn != want {
			t.Error("clientHandShake did not return newConn's Conn")
		}
		if gotNetConn != net.Conn(netConn) || gotReader != reader || gotWriter != writer {
			t.Errorf("newConn(%v, %v, %v, _), want (the net.Conn, the bufio.Reader, the bufio.Writer, _)", gotNetConn, gotReader, gotWriter)
		}
		// A client's peer is a server, so this Conn masks (RFC 6455 5.1), and it
		// follows the deflate agreement validateHandShakeResponseDeflate returned.
		if wantConfig := (NewConnConfig{PeerIsClient: false, PermessageDeflate: agreed}); gotConfig != wantConfig {
			t.Errorf("newConn config = %+v, want %+v", gotConfig, wantConfig)
		}
	})

	t.Run("returns newConn's error with the response and no Conn", func(t *testing.T) {
		var order []string
		di := newOrderTrackingDI(&order)
		wantErr := errors.New("socket gone")
		di.newConn = func(net.Conn, *bufio.Reader, *bufio.Writer, NewConnConfig) (*Conn, error) { return nil, wantErr }
		conn, resp, err := clientHandShake(nil, nil, nil, httptestRequest(t), ClientHandShakeConfig{}, di)
		if !errors.Is(err, wantErr) {
			t.Errorf("err = %v, want %v", err, wantErr)
		}
		if resp == nil || resp.StatusCode != 101 {
			t.Error("expected the upgrade response the server sent")
		}
		if conn != nil {
			t.Error("expected no Conn when newConn fails")
		}
	})

	// RFC 6455 7.1.7: a 4.1 Fail closes the connection. The errors are
	// wrapped, as the real ones are.
	closes := []struct {
		name           string
		validateErr    error
		subprotocolErr error
	}{
		{
			name:        "a 101 that is not HTTP/1.1",
			validateErr: fmt.Errorf("proto: %w", ErrHandshakeResponseProtoInvalid),
		},
		{
			name:        "a wrong Upgrade",
			validateErr: fmt.Errorf("upgrade: %w", ErrHandshakeResponseUpgradeHeaderInvalid),
		},
		{
			name:        "a wrong Sec-WebSocket-Accept",
			validateErr: fmt.Errorf("accept: %w", ErrHandshakeResponseAcceptHeaderMismatch),
		},
		{
			name:           "a repeated Sec-WebSocket-Protocol header",
			subprotocolErr: fmt.Errorf("subprotocol: %w", ErrHandshakeResponseSubprotocolRepeated),
		},
		{
			name:           "more than one subprotocol",
			subprotocolErr: fmt.Errorf("subprotocol: %w", ErrHandshakeResponseSubprotocolMultiple),
		},
		{
			name:           "a subprotocol never offered",
			subprotocolErr: fmt.Errorf("subprotocol: %w", ErrHandshakeResponseSubprotocolNotOffered),
		},
	}
	for _, testCase := range closes {
		t.Run("closes the net.Conn on "+testCase.name, func(t *testing.T) {
			var order []string
			di := newOrderTrackingDI(&order)
			if testCase.validateErr != nil {
				di.validateHandShakeResponse = func(*http.Response, string) error { return testCase.validateErr }
			}
			if testCase.subprotocolErr != nil {
				di.validateHandShakeResponseSubprotocol = func(*http.Response, *http.Request) error { return testCase.subprotocolErr }
			}
			conn := newFakeConn(nil)

			if _, _, err := clientHandShake(conn, nil, nil, httptestRequest(t), ClientHandShakeConfig{}, di); err == nil {
				t.Fatal("expected an error")
			}
			if !conn.closed {
				t.Error("the net.Conn was left open")
			}
		})
	}

	// Every other error leaves the net.Conn to the caller. A status other than
	// 101 is handled as plain HTTP (4.1), so it is not a Fail.
	leavesOpen := []struct {
		name           string
		sendErr        error
		readErr        error
		validateErr    error
		subprotocolErr error
		newConnErr     error
	}{
		{
			name:           "a subprotocol error that is not a 4.1 Fail",
			subprotocolErr: errors.New("other"),
		},
		{
			name:        "a status other than 101",
			validateErr: fmt.Errorf("status: %w", ErrHandshakeResponseStatusCodeInvalid),
		},
		{
			name:    "a failed send",
			sendErr: errors.New("broken pipe"),
		},
		{
			name:    "a failed read",
			readErr: errors.New("connection reset"),
		},
		{
			name:       "a failed newConn",
			newConnErr: errors.New("socket gone"),
		},
	}
	for _, testCase := range leavesOpen {
		t.Run("leaves the net.Conn open on "+testCase.name, func(t *testing.T) {
			var order []string
			di := newOrderTrackingDI(&order)
			if testCase.sendErr != nil {
				di.sendHandShakeRequest = func(io.Writer, *http.Request) error { return testCase.sendErr }
			}
			if testCase.readErr != nil {
				di.readHandShakeResponse = func(*bufio.Reader, *http.Request) (*http.Response, error) { return nil, testCase.readErr }
			}
			if testCase.validateErr != nil {
				di.validateHandShakeResponse = func(*http.Response, string) error { return testCase.validateErr }
			}
			if testCase.subprotocolErr != nil {
				di.validateHandShakeResponseSubprotocol = func(*http.Response, *http.Request) error { return testCase.subprotocolErr }
			}
			if testCase.newConnErr != nil {
				di.newConn = func(net.Conn, *bufio.Reader, *bufio.Writer, NewConnConfig) (*Conn, error) {
					return nil, testCase.newConnErr
				}
			}
			conn := newFakeConn(nil)

			if _, _, err := clientHandShake(conn, nil, nil, httptestRequest(t), ClientHandShakeConfig{}, di); err == nil {
				t.Fatal("expected an error")
			}
			if conn.closed {
				t.Error("the net.Conn was closed; it is the caller's to close")
			}
		})
	}

	failures := []struct {
		name      string
		breakStep string
		wantOrder string
	}{
		{"upgrade failure stops everything", "upgrade", "upgrade"},
		{"send failure stops the read", "send", "upgrade,send"},
		{"read failure stops validation", "read", "upgrade,send,read"},
		{"validation failure stops newConn", "validate", "upgrade,send,read,validate"},
		{"subprotocol failure stops newConn", "validateSubprotocol", "upgrade,send,read,validate,validateSubprotocol"},
		{"newConn failure surfaces", "newConn", "upgrade,send,read,validate,validateSubprotocol,validateDeflate,newConn"},
	}
	for _, testCase := range failures {
		t.Run(testCase.name, func(t *testing.T) {
			var order []string
			di := newOrderTrackingDI(&order)
			want := errors.New(testCase.breakStep + " failed")
			switch testCase.breakStep {
			case "upgrade":
				di.upgradeRequest = func(*http.Request, ClientHandShakeConfig) error {
					order = append(order, "upgrade")
					return want
				}
			case "send":
				di.sendHandShakeRequest = func(io.Writer, *http.Request) error {
					order = append(order, "send")
					return want
				}
			case "read":
				di.readHandShakeResponse = func(*bufio.Reader, *http.Request) (*http.Response, error) {
					order = append(order, "read")
					return nil, want
				}
			case "validate":
				di.validateHandShakeResponse = func(*http.Response, string) error {
					order = append(order, "validate")
					return want
				}
			case "validateSubprotocol":
				di.validateHandShakeResponseSubprotocol = func(*http.Response, *http.Request) error {
					order = append(order, "validateSubprotocol")
					return want
				}
			case "newConn":
				di.newConn = func(net.Conn, *bufio.Reader, *bufio.Writer, NewConnConfig) (*Conn, error) {
					order = append(order, "newConn")
					return nil, want
				}
			}
			if _, _, err := clientHandShake(newFakeConn(nil), nil, nil, httptestRequest(t), ClientHandShakeConfig{}, di); !errors.Is(err, want) {
				t.Fatalf("err = %v, want %v", err, want)
			}
			if strings.Join(order, ",") != testCase.wantOrder {
				t.Errorf("order = %v, want %s", order, testCase.wantOrder)
			}
		})
	}
}

func TestUpgradeRequest(t *testing.T) {
	// newRequest is a plain ws:// request, built without http.NewRequest so it
	// cannot fail.
	newRequest := func() *http.Request {
		return &http.Request{
			Method: "GET",
			URL:    &url.URL{Scheme: "ws", Host: "localhost:8001", Path: "/"},
			Header: http.Header{},
			Host:   "localhost:8001",
		}
	}

	t.Run("sets the four websocket headers", func(t *testing.T) {
		request := newRequest()
		err := upgradeRequest(request, ClientHandShakeConfig{}, upgradeRequestDI{
			generateWebSocketKey: func() string { return "FIXED-KEY" },
			addSubprotocols:      func(*http.Request, []string) {},
			addReqDeflateOffers:  func(*http.Request, []PermessageDeflateConfig) error { return nil },
		})
		if err != nil {
			t.Fatalf("upgradeRequest: %v", err)
		}
		want := map[string]string{
			"Upgrade":               "websocket",
			"Connection":            "Upgrade",
			"Sec-Websocket-Key":     "FIXED-KEY",
			"Sec-Websocket-Version": "13",
		}
		for headerKey, wantValue := range want {
			if got := request.Header.Get(headerKey); got != wantValue {
				t.Errorf("header %s = %q, want %q", headerKey, got, wantValue)
			}
		}
	})

	// 4.1: the method MUST be GET and the version at least HTTP/1.1.
	t.Run("sets the method to GET, the version to HTTP/1.1 and no body", func(t *testing.T) {
		request := newRequest()
		request.Method = "POST"
		request.Proto, request.ProtoMajor, request.ProtoMinor = "HTTP/1.0", 1, 0
		request.Body = io.NopCloser(strings.NewReader("hello"))
		err := upgradeRequest(request, ClientHandShakeConfig{}, upgradeRequestDI{
			generateWebSocketKey: func() string { return "FIXED-KEY" },
			addSubprotocols:      func(*http.Request, []string) {},
			addReqDeflateOffers:  func(*http.Request, []PermessageDeflateConfig) error { return nil },
		})
		if err != nil {
			t.Fatalf("upgradeRequest: %v", err)
		}
		if request.Method != "GET" {
			t.Errorf("method = %q, want GET", request.Method)
		}
		if request.Proto != "HTTP/1.1" || request.ProtoMajor != 1 || request.ProtoMinor != 1 {
			t.Errorf("version = %q (%d.%d), want HTTP/1.1 (1.1)", request.Proto, request.ProtoMajor, request.ProtoMinor)
		}
		if request.Body != nil {
			t.Errorf("body = %v, want nil", request.Body)
		}
	})

	t.Run("rejects a nil request", func(t *testing.T) {
		err := upgradeRequest(nil, ClientHandShakeConfig{}, upgradeRequestDI{
			generateWebSocketKey: func() string {
				t.Fatal("key generation must not run for a nil request")
				return ""
			},
			addSubprotocols: func(*http.Request, []string) {
				t.Fatal("no subprotocol must be added to a nil request")
			},
			addReqDeflateOffers: func(*http.Request, []PermessageDeflateConfig) error {
				t.Fatal("no offer must be added to a nil request")
				return nil
			},
		})
		if !errors.Is(err, ErrHandshakeRequestNil) {
			t.Errorf("err = %v, want %v", err, ErrHandshakeRequestNil)
		}
	})

	t.Run("rejects a request with no host", func(t *testing.T) {
		cases := []struct {
			name string
			url  *url.URL
		}{
			{name: "no url", url: nil},
			{name: "a url with an empty host", url: &url.URL{Scheme: "ws", Path: "/chat"}},
		}
		for _, testCase := range cases {
			request := &http.Request{Method: "GET", URL: testCase.url, Header: http.Header{}}
			err := upgradeRequest(request, ClientHandShakeConfig{}, upgradeRequestDI{
				generateWebSocketKey: func() string {
					t.Errorf("%s: key generation must not run without a host", testCase.name)
					return ""
				},
				addSubprotocols: func(*http.Request, []string) {
					t.Errorf("%s: no subprotocol must be added without a host", testCase.name)
				},
				addReqDeflateOffers: func(*http.Request, []PermessageDeflateConfig) error {
					t.Errorf("%s: no offer must be added without a host", testCase.name)
					return nil
				},
			})
			if !errors.Is(err, ErrUrlHostEmpty) {
				t.Errorf("%s: err = %v, want %v", testCase.name, err, ErrUrlHostEmpty)
			}
		}
	})

	t.Run("calls addSubprotocols with the config's subprotocols", func(t *testing.T) {
		request := newRequest()
		config := ClientHandShakeConfig{Subprotocols: []string{"chat"}}
		var gotRequest *http.Request
		var gotSubprotocols []string
		err := upgradeRequest(request, config, upgradeRequestDI{
			generateWebSocketKey: func() string { return "FIXED-KEY" },
			addSubprotocols: func(givenRequest *http.Request, subprotocols []string) {
				gotRequest, gotSubprotocols = givenRequest, subprotocols
			},
			addReqDeflateOffers: func(*http.Request, []PermessageDeflateConfig) error { return nil },
		})
		if err != nil {
			t.Fatalf("upgradeRequest: %v", err)
		}
		if gotRequest != request || !reflect.DeepEqual(gotSubprotocols, config.Subprotocols) {
			t.Errorf("addSubprotocols(%v, %q), want (the request, %q)", gotRequest, gotSubprotocols, config.Subprotocols)
		}
	})

	t.Run("calls addReqDeflateOffers with the config's PermessageDeflates", func(t *testing.T) {
		request := newRequest()
		config := ClientHandShakeConfig{PermessageDeflates: []PermessageDeflateConfig{{}}}
		var gotRequest *http.Request
		var gotPremsgConfigs []PermessageDeflateConfig
		err := upgradeRequest(request, config, upgradeRequestDI{
			generateWebSocketKey: func() string { return "FIXED-KEY" },
			addSubprotocols:      func(*http.Request, []string) {},
			addReqDeflateOffers: func(givenRequest *http.Request, premsg_configs []PermessageDeflateConfig) error {
				gotRequest, gotPremsgConfigs = givenRequest, premsg_configs
				return nil
			},
		})
		if err != nil {
			t.Fatalf("upgradeRequest: %v", err)
		}
		if gotRequest != request || !reflect.DeepEqual(gotPremsgConfigs, config.PermessageDeflates) {
			t.Errorf("addReqDeflateOffers(%v, %+v), want (the request, %+v)", gotRequest, gotPremsgConfigs, config.PermessageDeflates)
		}
	})

	t.Run("returns addReqDeflateOffers's error", func(t *testing.T) {
		want := errors.New("bad offer")
		err := upgradeRequest(newRequest(), ClientHandShakeConfig{}, upgradeRequestDI{
			generateWebSocketKey: func() string { return "FIXED-KEY" },
			addSubprotocols:      func(*http.Request, []string) {},
			addReqDeflateOffers:  func(*http.Request, []PermessageDeflateConfig) error { return want },
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})

	t.Run("real wiring produces a usable key", func(t *testing.T) {
		request := newRequest()
		if err := UpgradeRequest(request, ClientHandShakeConfig{}); err != nil {
			t.Fatalf("UpgradeRequest: %v", err)
		}
		key := request.Header.Get("Sec-WebSocket-Key")
		raw, err := base64.StdEncoding.DecodeString(key)
		if err != nil {
			t.Fatalf("key %q is not base64: %v", key, err)
		}
		if len(raw) != 16 {
			t.Errorf("decoded key is %d bytes, RFC 6455 requires 16", len(raw))
		}
	})
}

func TestAddSubprotocols(t *testing.T) {
	// newRequest is a plain ws:// request, built without http.NewRequest so it
	// cannot fail.
	newRequest := func() *http.Request {
		return &http.Request{
			Method: "GET",
			URL:    &url.URL{Scheme: "ws", Host: "localhost:8001", Path: "/"},
			Header: http.Header{},
			Host:   "localhost:8001",
		}
	}

	// RFC 6455 4.1: subprotocols are offered in Sec-WebSocket-Protocol, in
	// order of preference.
	t.Run("appends the config's subprotocols after the request's own line", func(t *testing.T) {
		cases := []struct {
			name               string
			originLines        []string
			configSubprotocols []string
			want               []string
		}{
			{name: "no config subprotocols, no line in the request"},
			{
				name:        "no config subprotocols leaves the request's line alone",
				originLines: []string{"mine"},
				want:        []string{"mine"},
			},
			{
				name:               "no line in the request",
				configSubprotocols: []string{"chat", "superchat"},
				want:               []string{"chat, superchat"},
			},
			{
				name:               "the request's line stays first",
				originLines:        []string{"mine"},
				configSubprotocols: []string{"chat"},
				want:               []string{"mine", "chat"},
			},
			{
				name:               "one already in the request is not added again",
				originLines:        []string{"chat, mine"},
				configSubprotocols: []string{"chat", "superchat"},
				want:               []string{"chat, mine", "superchat"},
			},
			{
				name:               "a repeat in the config is added once",
				configSubprotocols: []string{"chat", "chat"},
				want:               []string{"chat"},
			},
			{
				name:               "nothing new adds no line",
				originLines:        []string{"chat"},
				configSubprotocols: []string{"chat"},
				want:               []string{"chat"},
			},
		}
		for _, testCase := range cases {
			request := newRequest()
			for _, line := range testCase.originLines {
				request.Header.Add("Sec-WebSocket-Protocol", line)
			}
			addSubprotocols(request, testCase.configSubprotocols)
			if got := request.Header.Values("Sec-WebSocket-Protocol"); !reflect.DeepEqual(got, testCase.want) {
				t.Errorf("%s: Sec-WebSocket-Protocol = %q, want %q", testCase.name, got, testCase.want)
			}
		}
	})
}

func TestAddReqDeflateOffers(t *testing.T) {
	// newRequest is a plain ws:// request, built without http.NewRequest so it
	// cannot fail.
	newRequest := func() *http.Request {
		return &http.Request{
			Method: "GET",
			URL:    &url.URL{Scheme: "ws", Host: "localhost:8001", Path: "/"},
			Header: http.Header{},
			Host:   "localhost:8001",
		}
	}

	// RFC 7692 5: the offers go in Sec-WebSocket-Extensions, in order of
	// preference.
	t.Run("appends the config's offers after the request's own line", func(t *testing.T) {
		cases := []struct {
			name           string
			originLines    []string
			configDeflates []PermessageDeflateConfig
			want           []string
		}{
			{name: "no config offers, no line in the request"},
			{
				name:        "no config offers leaves the request's line alone",
				originLines: []string{"x-custom"},
				want:        []string{"x-custom"},
			},
			{
				name: "several offers in order on one line",
				configDeflates: []PermessageDeflateConfig{
					{ClientNoContextTakeover: true},
					{},
				},
				want: []string{"permessage-deflate; client_no_context_takeover, permessage-deflate"},
			},
			{
				name:           "the request's original line stays first",
				originLines:    []string{"x-custom"},
				configDeflates: []PermessageDeflateConfig{{}},
				want:           []string{"x-custom", "permessage-deflate"},
			},
		}
		for _, testCase := range cases {
			request := newRequest()
			for _, line := range testCase.originLines {
				request.Header.Add("Sec-WebSocket-Extensions", line)
			}
			if err := addReqDeflateOffers(request, testCase.configDeflates); err != nil {
				t.Fatalf("%s: addReqDeflateOffers: %v", testCase.name, err)
			}
			if got := request.Header.Values("Sec-WebSocket-Extensions"); !reflect.DeepEqual(got, testCase.want) {
				t.Errorf("%s: Sec-WebSocket-Extensions = %q, want %q", testCase.name, got, testCase.want)
			}
		}
	})

	// RFC 7692 7.1: in an offer the client is "my" side and the server is the
	// peer.
	t.Run("writes each field as its offer param", func(t *testing.T) {
		cases := []struct {
			name         string
			premsgConfig PermessageDeflateConfig
			want         string
		}{
			{
				name:         "no params",
				premsgConfig: PermessageDeflateConfig{},
				want:         "permessage-deflate",
			},
			{
				name:         "ServerNoContextTakeover",
				premsgConfig: PermessageDeflateConfig{ServerNoContextTakeover: true},
				want:         "permessage-deflate; server_no_context_takeover",
			},
			{
				name:         "ClientNoContextTakeover",
				premsgConfig: PermessageDeflateConfig{ClientNoContextTakeover: true},
				want:         "permessage-deflate; client_no_context_takeover",
			},
			{
				name:         "ServerMaxWindowBits",
				premsgConfig: PermessageDeflateConfig{ServerMaxWindowBits: 10},
				want:         "permessage-deflate; server_max_window_bits=10",
			},
			{
				name:         "ClientMaxWindowBits without ClientMaxWindowBitsEnabled is not sent",
				premsgConfig: PermessageDeflateConfig{ClientMaxWindowBits: 8},
				want:         "permessage-deflate",
			},
			{
				name:         "an invalid ClientMaxWindowBits without ClientMaxWindowBitsEnabled is not checked",
				premsgConfig: PermessageDeflateConfig{ClientMaxWindowBits: 16},
				want:         "permessage-deflate",
			},
			{
				name: "every param",
				premsgConfig: PermessageDeflateConfig{
					ClientNoContextTakeover: true,
					ServerNoContextTakeover: true,
					ServerMaxWindowBits:     10,
				},
				want: "permessage-deflate; server_no_context_takeover; client_no_context_takeover; server_max_window_bits=10",
			},
		}
		for _, testCase := range cases {
			request := newRequest()
			if err := addReqDeflateOffers(request, []PermessageDeflateConfig{testCase.premsgConfig}); err != nil {
				t.Fatalf("%s: addReqDeflateOffers: %v", testCase.name, err)
			}
			if got := request.Header.Get("Sec-WebSocket-Extensions"); got != testCase.want {
				t.Errorf("%s: Sec-WebSocket-Extensions = %q, want %q", testCase.name, got, testCase.want)
			}
		}
	})

	// RFC 7692 7.1.2: window bits are 8 to 15.
	t.Run("accepts window bits of 8 to 15", func(t *testing.T) {
		for bits := uint(8); bits <= 15; bits++ {
			request := newRequest()
			err := addReqDeflateOffers(request, []PermessageDeflateConfig{{ServerMaxWindowBits: bits}})
			if err != nil {
				t.Fatalf("%d: addReqDeflateOffers: %v", bits, err)
			}
			want := fmt.Sprintf("permessage-deflate; server_max_window_bits=%d", bits)
			if got := request.Header.Get("Sec-WebSocket-Extensions"); got != want {
				t.Errorf("%d: Sec-WebSocket-Extensions = %q, want %q", bits, got, want)
			}
		}
	})

	t.Run("rejects window bits other than 0 or 8 to 15 and adds no offer", func(t *testing.T) {
		for _, bits := range []uint{1, 2, 3, 4, 5, 6, 7, 16, 17, 255, ^uint(0)} {
			cases := []struct {
				name           string
				configDeflates []PermessageDeflateConfig
			}{
				{
					name:           fmt.Sprintf("only ServerMaxWindowBits %d", bits),
					configDeflates: []PermessageDeflateConfig{{ServerMaxWindowBits: bits}},
				},
				{
					name:           fmt.Sprintf("a valid offer before ServerMaxWindowBits %d", bits),
					configDeflates: []PermessageDeflateConfig{{}, {ServerMaxWindowBits: bits}},
				},
				{
					name:           fmt.Sprintf("a valid offer after ServerMaxWindowBits %d", bits),
					configDeflates: []PermessageDeflateConfig{{ServerMaxWindowBits: bits}, {}},
				},
			}
			for _, testCase := range cases {
				request := newRequest()
				err := addReqDeflateOffers(request, testCase.configDeflates)
				if !errors.Is(err, ErrHandshakeRequestDeflateWindowBitsInvalid) {
					t.Errorf("%s: err = %v, want %v", testCase.name, err, ErrHandshakeRequestDeflateWindowBitsInvalid)
				}
				if got := request.Header.Values("Sec-WebSocket-Extensions"); got != nil {
					t.Errorf("%s: Sec-WebSocket-Extensions = %q, want none", testCase.name, got)
				}
			}
		}
	})

	t.Run("rejects ClientMaxWindowBitsEnabled and adds no offer", func(t *testing.T) {
		cases := []struct {
			name           string
			configDeflates []PermessageDeflateConfig
		}{
			{
				name:           "with ClientMaxWindowBits 0",
				configDeflates: []PermessageDeflateConfig{{ClientMaxWindowBitsEnabled: true}},
			},
			{
				name:           "with ClientMaxWindowBits 8",
				configDeflates: []PermessageDeflateConfig{{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 8}},
			},
			{
				name:           "with ClientMaxWindowBits 15",
				configDeflates: []PermessageDeflateConfig{{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 15}},
			},
			{
				name:           "after a valid offer",
				configDeflates: []PermessageDeflateConfig{{}, {ClientMaxWindowBitsEnabled: true}},
			},
			{
				name:           "before a valid offer",
				configDeflates: []PermessageDeflateConfig{{ClientMaxWindowBitsEnabled: true}, {}},
			},
		}
		for _, testCase := range cases {
			request := newRequest()
			err := addReqDeflateOffers(request, testCase.configDeflates)
			if !errors.Is(err, ErrHandshakeRequestDeflateUnsupported) {
				t.Errorf("%s: err = %v, want %v", testCase.name, err, ErrHandshakeRequestDeflateUnsupported)
			}
			if got := request.Header.Values("Sec-WebSocket-Extensions"); got != nil {
				t.Errorf("%s: Sec-WebSocket-Extensions = %q, want none", testCase.name, got)
			}
		}
	})
}

func TestSendHandShakeRequest(t *testing.T) {
	t.Run("writes the plain http message", func(t *testing.T) {
		w := &fakeIOWriter{}
		err := sendHandShakeRequest(w, httptestRequest(t), sendHandShakeRequestDI{
			requestToPlainHTTPMsg: func(*http.Request) (string, error) {
				return "GET / HTTP/1.1\r\n\r\n", nil
			},
		})
		if err != nil {
			t.Fatalf("sendHandShakeRequest: %v", err)
		}
		if w.String() != "GET / HTTP/1.1\r\n\r\n" {
			t.Errorf("written = %q", w.String())
		}
	})

	t.Run("rejects a nil request", func(t *testing.T) {
		err := sendHandShakeRequest(&fakeIOWriter{}, nil, sendHandShakeRequestDI{
			requestToPlainHTTPMsg: func(*http.Request) (string, error) {
				t.Fatal("must not run for a nil request")
				return "", nil
			},
		})
		if !errors.Is(err, ErrHandshakeRequestNil) {
			t.Errorf("err = %v, want %v", err, ErrHandshakeRequestNil)
		}
	})

	t.Run("propagates a serialisation error", func(t *testing.T) {
		want := errors.New("cannot serialise")
		err := sendHandShakeRequest(&fakeIOWriter{}, httptestRequest(t), sendHandShakeRequestDI{
			requestToPlainHTTPMsg: func(*http.Request) (string, error) { return "", want },
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})

	t.Run("propagates a write error", func(t *testing.T) {
		want := errors.New("broken pipe")
		w := &fakeIOWriter{err: want}
		err := sendHandShakeRequest(w, httptestRequest(t), sendHandShakeRequestDI{
			requestToPlainHTTPMsg: func(*http.Request) (string, error) { return "x", nil },
		})
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestReadHandShakeResponse(t *testing.T) {
	t.Run("returns the parsed response", func(t *testing.T) {
		want := &http.Response{StatusCode: 101}
		res, err := readHandShakeResponse(bufio.NewReader(&fakeIOReader{}), httptestRequest(t), readHandShakeResponseDI{
			httpReadResponse: func(*bufio.Reader, *http.Request) (*http.Response, error) {
				return want, nil
			},
		})
		if err != nil {
			t.Fatalf("readHandShakeResponse: %v", err)
		}
		if res != want {
			t.Error("response was not returned")
		}
	})

	t.Run("rejects a nil request", func(t *testing.T) {
		_, err := readHandShakeResponse(bufio.NewReader(&fakeIOReader{}), nil, readHandShakeResponseDI{
			httpReadResponse: func(*bufio.Reader, *http.Request) (*http.Response, error) {
				t.Fatal("must not run for a nil request")
				return nil, nil
			},
		})
		if !errors.Is(err, ErrHandshakeRequestNil) {
			t.Errorf("err = %v, want %v", err, ErrHandshakeRequestNil)
		}
	})

	t.Run("propagates a parse error", func(t *testing.T) {
		want := errors.New("malformed response")
		_, err := readHandShakeResponse(bufio.NewReader(&fakeIOReader{}), httptestRequest(t), readHandShakeResponseDI{
			httpReadResponse: func(*bufio.Reader, *http.Request) (*http.Response, error) {
				return nil, want
			},
		})
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	})

	t.Run("real wiring parses bytes off the reader", func(t *testing.T) {
		raw := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n\r\n"
		r := &fakeIOReader{}
		r.Reset([]byte(raw))
		res, err := ReadHandShakeResponse(bufio.NewReader(r), httptestRequest(t))
		if err != nil {
			t.Fatalf("ReadHandShakeResponse: %v", err)
		}
		if res.StatusCode != 101 {
			t.Errorf("StatusCode = %d, want 101", res.StatusCode)
		}
		if res.Header.Get("Upgrade") != "websocket" {
			t.Errorf("Upgrade = %q", res.Header.Get("Upgrade"))
		}
	})
}

func validHandShakeResponse(key string) *http.Response {
	response := &http.Response{
		StatusCode: 101,
		Proto:      "HTTP/1.1",
		Header:     http.Header{},
	}
	response.Header.Set("Connection", "Upgrade")
	response.Header.Set("Upgrade", "websocket")
	response.Header.Set("Sec-WebSocket-Accept", GenerateSecWebsocketAccept(key))
	return response
}

func TestValidateHandShakeResponse(t *testing.T) {
	const key = "dGhlIHNhbXBsZSBub25jZQ=="

	t.Run("accepts a well formed response", func(t *testing.T) {
		if err := ValidateHandShakeResponse(validHandShakeResponse(key), key); err != nil {
			t.Errorf("ValidateHandShakeResponse: %v", err)
		}
	})

	t.Run("header comparison is case insensitive", func(t *testing.T) {
		response := validHandShakeResponse(key)
		response.Header.Set("Connection", "UPGRADE")
		response.Header.Set("Upgrade", "WebSocket")
		if err := ValidateHandShakeResponse(response, key); err != nil {
			t.Errorf("ValidateHandShakeResponse: %v", err)
		}
	})

	// The server side of the same rule: a response carrying the token among
	// others is conforming, and refusing it would fail a working connection.
	t.Run("Connection is read as a token list", func(t *testing.T) {
		response := validHandShakeResponse(key)
		response.Header.Set("Connection", "keep-alive, Upgrade")
		if err := ValidateHandShakeResponse(response, key); err != nil {
			t.Errorf("ValidateHandShakeResponse: %v", err)
		}
	})

	t.Run("a field split across two lines", func(t *testing.T) {
		response := validHandShakeResponse(key)
		response.Header.Del("Connection")
		response.Header.Add("Connection", "keep-alive")
		response.Header.Add("Connection", "Upgrade")
		if err := ValidateHandShakeResponse(response, key); err != nil {
			t.Errorf("ValidateHandShakeResponse: %v", err)
		}
	})

	tests := []struct {
		name    string
		mutate  func(*http.Response)
		wantErr error
	}{
		{
			name:    "wrong status code",
			mutate:  func(r *http.Response) { r.StatusCode = 200 },
			wantErr: ErrHandshakeResponseStatusCodeInvalid,
		},
		{
			name:    "wrong protocol",
			mutate:  func(r *http.Response) { r.Proto = "HTTP/2.0" },
			wantErr: ErrHandshakeResponseProtoInvalid,
		},
		{
			name:    "connection header not upgrade",
			mutate:  func(r *http.Response) { r.Header.Set("Connection", "keep-alive") },
			wantErr: ErrHandshakeResponseConnectionHeaderInvalid,
		},
		{
			name:    "upgrade header not websocket",
			mutate:  func(r *http.Response) { r.Header.Set("Upgrade", "h2c") },
			wantErr: ErrHandshakeResponseUpgradeHeaderInvalid,
		},
		{
			name:    "upgrade header carries websocket among others",
			mutate:  func(r *http.Response) { r.Header.Set("Upgrade", "websocket, h2c") },
			wantErr: ErrHandshakeResponseUpgradeHeaderInvalid,
		},
		{
			name:    "upgrade header split across two lines",
			mutate:  func(r *http.Response) { r.Header.Add("Upgrade", "h2c") },
			wantErr: ErrHandshakeResponseUpgradeHeaderInvalid,
		},
		{
			name:    "upgrade header missing",
			mutate:  func(r *http.Response) { r.Header.Del("Upgrade") },
			wantErr: ErrHandshakeResponseUpgradeHeaderInvalid,
		},
		{
			name:    "missing accept header",
			mutate:  func(r *http.Response) { r.Header.Del("Sec-WebSocket-Accept") },
			wantErr: ErrHandshakeResponseAcceptHeaderMissing,
		},
		{
			name:    "accept header does not match the key",
			mutate:  func(r *http.Response) { r.Header.Set("Sec-WebSocket-Accept", "wrong") },
			wantErr: ErrHandshakeResponseAcceptHeaderMismatch,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			response := validHandShakeResponse(key)
			testCase.mutate(response)
			err := ValidateHandShakeResponse(response, key)
			if !errors.Is(err, testCase.wantErr) {
				t.Errorf("err = %v, want %v", err, testCase.wantErr)
			}
		})
	}

	t.Run("rejects an accept value computed from a different key", func(t *testing.T) {
		response := validHandShakeResponse(key)
		if err := ValidateHandShakeResponse(response, "a-different-key"); !errors.Is(err, ErrHandshakeResponseAcceptHeaderMismatch) {
			t.Errorf("err = %v, want %v", err, ErrHandshakeResponseAcceptHeaderMismatch)
		}
	})
}

func TestClientMustFailConnection(t *testing.T) {
	// Every 4.1 check answered with "Fail the WebSocket Connection".
	fails := []error{
		ErrHandshakeResponseProtoInvalid,
		ErrHandshakeResponseUpgradeHeaderInvalid,
		ErrHandshakeResponseConnectionHeaderInvalid,
		ErrHandshakeResponseAcceptHeaderMissing,
		ErrHandshakeResponseAcceptHeaderMismatch,
		ErrHandshakeResponseSubprotocolRepeated,
		ErrHandshakeResponseSubprotocolMultiple,
		ErrHandshakeResponseSubprotocolNotOffered,
	}
	for _, sentinel := range fails {
		if !ClientMustFailConnection(fmt.Errorf("wrapped: %w", sentinel)) {
			t.Errorf("ClientMustFailConnection(%v) = false, want true", sentinel)
		}
	}

	// A status other than 101 is plain HTTP (4.1), and the rest are not 4.1
	// response checks at all.
	others := []error{
		ErrHandshakeResponseStatusCodeInvalid,
		errors.New("an error from outside this package"),
	}
	for _, other := range others {
		if ClientMustFailConnection(fmt.Errorf("wrapped: %w", other)) {
			t.Errorf("ClientMustFailConnection(%v) = true, want false", other)
		}
	}
}

func TestValidateHandShakeResponseSubprotocol(t *testing.T) {
	// In every case below, each string is one header line, and a line may hold
	// a comma list. offered is the client's request, answered the server's
	// response.

	passes := []struct {
		name     string
		offered  []string
		answered []string
	}{
		{
			// No header at all means the server picked none (RFC 6455 4.2.2).
			name:     "the server picked none",
			offered:  []string{"chat.v2"},
			answered: nil,
		},
		{
			name:     "nothing offered and nothing answered",
			offered:  nil,
			answered: nil,
		},
		{
			name:     "one value the client offered",
			offered:  []string{"chat.v2, chat.v1"},
			answered: []string{"chat.v1"},
		},
		{
			name:     "one value from an offer split across two lines",
			offered:  []string{"chat.v2", "chat.v1"},
			answered: []string{"chat.v1"},
		},
	}
	for _, testCase := range passes {
		t.Run("passes: "+testCase.name, func(t *testing.T) {
			request := httptestRequest(t)
			for _, line := range testCase.offered {
				request.Header.Add("Sec-WebSocket-Protocol", line)
			}
			resp := &http.Response{Header: http.Header{}}
			for _, line := range testCase.answered {
				resp.Header.Add("Sec-WebSocket-Protocol", line)
			}

			if err := ValidateHandShakeResponseSubprotocol(resp, request); err != nil {
				t.Errorf("ValidateHandShakeResponseSubprotocol: %v", err)
			}
		})
	}

	fails := []struct {
		name     string
		offered  []string
		answered []string
		wantErr  error
	}{
		{
			name:     "multiple error - a list on one line",
			offered:  []string{"chat.v2, chat.v1"},
			answered: []string{"chat.v2, chat.v1"},
			wantErr:  ErrHandshakeResponseSubprotocolMultiple,
		},
		{
			name:     "repeated error - two lines",
			offered:  []string{"chat.v2, chat.v1"},
			answered: []string{"chat.v2", "chat.v1"},
			wantErr:  ErrHandshakeResponseSubprotocolRepeated,
		},
		{
			name:     "repeated error - two empty lines",
			offered:  []string{"chat.v2"},
			answered: []string{"", ""},
			wantErr:  ErrHandshakeResponseSubprotocolRepeated,
		},
		{
			// Neither value was offered, but more than one is checked first.
			name:     "multiple error - more than one value, none offered",
			offered:  nil,
			answered: []string{"chat.v2, chat.v9"},
			wantErr:  ErrHandshakeResponseSubprotocolMultiple,
		},
		{
			name:     "not offered error - a value never offered",
			offered:  []string{"chat.v2"},
			answered: []string{"chat.v9"},
			wantErr:  ErrHandshakeResponseSubprotocolNotOffered,
		},
		{
			name:     "not offered error - a value when nothing was offered",
			offered:  nil,
			answered: []string{"chat.v2"},
			wantErr:  ErrHandshakeResponseSubprotocolNotOffered,
		},
		{
			// An empty header is still one line, unlike no header at all: the
			// server sent something, just something invalid.
			name:     "not offered error - an empty value",
			offered:  []string{"chat.v2"},
			answered: []string{""},
			wantErr:  ErrHandshakeResponseSubprotocolNotOffered,
		},
		{
			name:     "not offered error - a value that differs only in case",
			offered:  []string{"chat.v2"},
			answered: []string{"CHAT.V2"},
			wantErr:  ErrHandshakeResponseSubprotocolNotOffered,
		},
	}
	for _, testCase := range fails {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptestRequest(t)
			for _, line := range testCase.offered {
				request.Header.Add("Sec-WebSocket-Protocol", line)
			}
			resp := &http.Response{Header: http.Header{}}
			for _, line := range testCase.answered {
				resp.Header.Add("Sec-WebSocket-Protocol", line)
			}

			err := ValidateHandShakeResponseSubprotocol(resp, request)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("err = %v, want it to wrap %v", err, testCase.wantErr)
			}
			// The message says what the server sent, not just the sentinel.
			if err.Error() == testCase.wantErr.Error() {
				t.Errorf("err = %q, want what the server sent around the sentinel", err)
			}
		})
	}
}

func TestValidateHandShakeResponseDeflate(t *testing.T) {
	base_di := func() validateHandShakeResponseDeflateDI {
		return validateHandShakeResponseDeflateDI{
			validateRespDeflateExt: func(*http.Response) (*PermessageDeflateConfig, error) {
				return &PermessageDeflateConfig{}, nil
			},
			validateRespDeflateExtWithReq: func(PermessageDeflateConfig, *http.Request) (PermessageDeflateConfig, error) {
				return PermessageDeflateConfig{}, nil
			},
		}
	}

	t.Run("passes the response to validateRespDeflateExt, its config and the request to validateRespDeflateExtWithReq, and returns validateRespDeflateExtWithReq's config", func(t *testing.T) {
		resp := &http.Response{}
		request := httptestRequest(t)
		server_config := &PermessageDeflateConfig{ServerMaxWindowBits: 10}
		want := PermessageDeflateConfig{ClientNoContextTakeover: true, ServerMaxWindowBits: 10}
		var gotResp *http.Response
		var gotConfig PermessageDeflateConfig
		var gotRequest *http.Request
		di := base_di()
		di.validateRespDeflateExt = func(givenResp *http.Response) (*PermessageDeflateConfig, error) {
			gotResp = givenResp
			return server_config, nil
		}
		di.validateRespDeflateExtWithReq = func(givenConfig PermessageDeflateConfig, givenRequest *http.Request) (PermessageDeflateConfig, error) {
			gotConfig, gotRequest = givenConfig, givenRequest
			return want, nil
		}

		agreed_config, err := validateHandShakeResponseDeflate(resp, request, di)
		if err != nil {
			t.Fatalf("validateHandShakeResponseDeflate: %v", err)
		}
		if agreed_config == nil || *agreed_config != want {
			t.Errorf("agreed_config = %+v, want validateRespDeflateExtWithReq's config %+v", agreed_config, want)
		}
		if gotResp != resp {
			t.Error("validateRespDeflateExt did not get the response")
		}
		if gotConfig != *server_config || gotRequest != request {
			t.Errorf("validateRespDeflateExtWithReq(%+v, %v), want (%+v, the request)", gotConfig, gotRequest, *server_config)
		}
	})

	t.Run("returns nil and no error when the server declined deflate", func(t *testing.T) {
		di := base_di()
		di.validateRespDeflateExt = func(*http.Response) (*PermessageDeflateConfig, error) { return nil, nil }
		di.validateRespDeflateExtWithReq = func(PermessageDeflateConfig, *http.Request) (PermessageDeflateConfig, error) {
			return PermessageDeflateConfig{}, errors.New("validateRespDeflateExtWithReq error")
		}

		agreed_config, err := validateHandShakeResponseDeflate(&http.Response{}, httptestRequest(t), di)
		if err != nil || agreed_config != nil {
			t.Errorf("validateHandShakeResponseDeflate = (%+v, %v), want (nil, nil)", agreed_config, err)
		}
	})

	t.Run("returns validateRespDeflateExt's error and no config", func(t *testing.T) {
		wantErr := errors.New("validateRespDeflateExt error")
		di := base_di()
		di.validateRespDeflateExt = func(*http.Response) (*PermessageDeflateConfig, error) {
			return &PermessageDeflateConfig{}, wantErr
		}
		di.validateRespDeflateExtWithReq = func(PermessageDeflateConfig, *http.Request) (PermessageDeflateConfig, error) {
			return PermessageDeflateConfig{}, errors.New("validateRespDeflateExtWithReq error")
		}

		agreed_config, err := validateHandShakeResponseDeflate(&http.Response{}, httptestRequest(t), di)
		if err != wantErr || agreed_config != nil {
			t.Errorf("validateHandShakeResponseDeflate = (%+v, %v), want (nil, %v)", agreed_config, err, wantErr)
		}
	})

	t.Run("returns validateRespDeflateExtWithReq's error and no config", func(t *testing.T) {
		wantErr := errors.New("validateRespDeflateExtWithReq error")
		di := base_di()
		di.validateRespDeflateExtWithReq = func(PermessageDeflateConfig, *http.Request) (PermessageDeflateConfig, error) {
			return PermessageDeflateConfig{ServerNoContextTakeover: true}, wantErr
		}

		agreed_config, err := validateHandShakeResponseDeflate(&http.Response{}, httptestRequest(t), di)
		if err != wantErr || agreed_config != nil {
			t.Errorf("validateHandShakeResponseDeflate = (%+v, %v), want (nil, %v)", agreed_config, err, wantErr)
		}
	})
}

func TestValidateRespDeflateExt(t *testing.T) {
	// Each string is one Sec-WebSocket-Extensions line of the response.
	passes := []struct {
		name       string
		extensions []string
		want       *PermessageDeflateConfig
	}{
		{
			name:       "no Sec-WebSocket-Extensions",
			extensions: nil,
			want:       nil,
		},
		{
			name:       "only another extension",
			extensions: []string{"x-other"},
			want:       nil,
		},
		{
			name:       "permessage-deflate with no params",
			extensions: []string{"permessage-deflate"},
			want:       &PermessageDeflateConfig{},
		},
		{
			name:       "permessage-deflate with every param",
			extensions: []string{"permessage-deflate; client_no_context_takeover; server_no_context_takeover; client_max_window_bits=12; server_max_window_bits=10"},
			want: &PermessageDeflateConfig{
				ClientNoContextTakeover:    true,
				ServerNoContextTakeover:    true,
				ClientMaxWindowBitsEnabled: true,
				ClientMaxWindowBits:        12,
				ServerMaxWindowBits:        10,
			},
		},
		{
			name:       "permessage-deflate after another extension on one line",
			extensions: []string{"x-other, permessage-deflate; client_no_context_takeover"},
			want:       &PermessageDeflateConfig{ClientNoContextTakeover: true},
		},
		{
			name:       "permessage-deflate on a line after another extension",
			extensions: []string{"x-other", "permessage-deflate; server_no_context_takeover"},
			want:       &PermessageDeflateConfig{ServerNoContextTakeover: true},
		},
	}
	for _, testCase := range passes {
		t.Run("passes: "+testCase.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{}}
			for _, line := range testCase.extensions {
				resp.Header.Add("Sec-WebSocket-Extensions", line)
			}

			server_config, err := validateRespDeflateExt(resp)
			if err != nil {
				t.Fatalf("validateRespDeflateExt: %v", err)
			}
			if !reflect.DeepEqual(server_config, testCase.want) {
				t.Errorf("server_config = %+v, want %+v", server_config, testCase.want)
			}
		})
	}

	fails := []struct {
		name       string
		extensions []string
		wantErr    error
	}{
		{
			// RFC 7692 5: the server accepts one offer at most.
			name:       "permessage-deflate twice on one line",
			extensions: []string{"permessage-deflate, permessage-deflate"},
			wantErr:    ErrHandshakeResponseExtensionNotOffered,
		},
		{
			name:       "permessage-deflate twice on two lines",
			extensions: []string{"permessage-deflate", "permessage-deflate"},
			wantErr:    ErrHandshakeResponseExtensionNotOffered,
		},
		{
			name:       "a param parseDeflateParams refuses",
			extensions: []string{"permessage-deflate; unknown_param"},
			wantErr:    ErrHandshakeResponseDeflateParamsInvalid,
		},
		{
			// RFC 7692 7.1.2.2: a response must give it a value.
			name:       "client_max_window_bits with no value",
			extensions: []string{"permessage-deflate; client_max_window_bits"},
			wantErr:    ErrHandshakeResponseDeflateParamsInvalid,
		},
	}
	for _, testCase := range fails {
		t.Run("fails: "+testCase.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{}}
			for _, line := range testCase.extensions {
				resp.Header.Add("Sec-WebSocket-Extensions", line)
			}

			server_config, err := validateRespDeflateExt(resp)
			if !errors.Is(err, testCase.wantErr) {
				t.Errorf("err = %v, want %v", err, testCase.wantErr)
			}
			if server_config != nil {
				t.Errorf("server_config = %+v, want nil", server_config)
			}
		})
	}
}

func TestValidateRespDeflateExtWithReq(t *testing.T) {
	// Each string in offers is one Sec-WebSocket-Extensions line of the
	// request.
	passes := []struct {
		name          string
		offers        []string
		server_config PermessageDeflateConfig
	}{
		{
			name:          "no params against an offer with none",
			offers:        []string{"permessage-deflate"},
			server_config: PermessageDeflateConfig{},
		},
		{
			// RFC 7692 7.1.1.1, 7.1.1.2, 7.1.2.1.
			name:   "both no_context_takeover flags and server_max_window_bits, none offered",
			offers: []string{"permessage-deflate"},
			server_config: PermessageDeflateConfig{
				ClientNoContextTakeover: true,
				ServerNoContextTakeover: true,
				ServerMaxWindowBits:     10,
			},
		},
		{
			name:          "client_max_window_bits, offered with no value",
			offers:        []string{"permessage-deflate; client_max_window_bits"},
			server_config: PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 10},
		},
		{
			name:          "client_max_window_bits equal to the offer's",
			offers:        []string{"permessage-deflate; client_max_window_bits=10"},
			server_config: PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 10},
		},
		{
			name:          "client_max_window_bits under the offer's",
			offers:        []string{"permessage-deflate; client_max_window_bits=12"},
			server_config: PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 10},
		},
		{
			name:          "server_max_window_bits equal to the offer's",
			offers:        []string{"permessage-deflate; server_max_window_bits=10"},
			server_config: PermessageDeflateConfig{ServerMaxWindowBits: 10},
		},
		{
			name:          "server_max_window_bits under the offer's",
			offers:        []string{"permessage-deflate; server_max_window_bits=10"},
			server_config: PermessageDeflateConfig{ServerMaxWindowBits: 8},
		},
		{
			name:          "the second offer allows it when the first does not",
			offers:        []string{"permessage-deflate, permessage-deflate; client_max_window_bits"},
			server_config: PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 10},
		},
		{
			name:          "the second offer on its own line allows it",
			offers:        []string{"permessage-deflate", "permessage-deflate; client_max_window_bits"},
			server_config: PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 10},
		},
		{
			// RFC 7692 7.1.1.1: the first offer needs server_no_context_takeover
			// back, so only the plain one allows this response.
			name:          "a response without server_no_context_takeover matches the plain fallback",
			offers:        []string{"permessage-deflate; server_no_context_takeover, permessage-deflate"},
			server_config: PermessageDeflateConfig{},
		},
		{
			// RFC 7692 7: an offer that does not parse does not count.
			name:          "an offer that does not parse is skipped",
			offers:        []string{"permessage-deflate; unknown_param, permessage-deflate"},
			server_config: PermessageDeflateConfig{},
		},
		{
			name:          "another extension in the request is skipped",
			offers:        []string{"x-other, permessage-deflate"},
			server_config: PermessageDeflateConfig{},
		},
	}
	for _, testCase := range passes {
		t.Run("passes: "+testCase.name, func(t *testing.T) {
			request := httptestRequest(t)
			for _, line := range testCase.offers {
				request.Header.Add("Sec-WebSocket-Extensions", line)
			}

			// No offer here has client_no_context_takeover, so the agreement is
			// the response as it is.
			agreed_config, err := validateRespDeflateExtWithReq(testCase.server_config, request)
			if err != nil {
				t.Fatalf("validateRespDeflateExtWithReq: %v", err)
			}
			if agreed_config != testCase.server_config {
				t.Errorf("agreed_config = %+v, want the response %+v", agreed_config, testCase.server_config)
			}
		})
	}

	// RFC 7692 7.1.1.2: an offered client_no_context_takeover binds the client
	// even when the response leaves it out.
	promises := []struct {
		name          string
		offers        []string
		server_config PermessageDeflateConfig
		want          PermessageDeflateConfig
	}{
		{
			name:          "the only offer has it and the response leaves it out",
			offers:        []string{"permessage-deflate; client_no_context_takeover"},
			server_config: PermessageDeflateConfig{ServerNoContextTakeover: true},
			want:          PermessageDeflateConfig{ServerNoContextTakeover: true, ClientNoContextTakeover: true},
		},
		{
			// The response does not say which offer it accepted.
			name:          "one of two offers that allow the response has it",
			offers:        []string{"permessage-deflate, permessage-deflate; client_no_context_takeover"},
			server_config: PermessageDeflateConfig{},
			want:          PermessageDeflateConfig{ClientNoContextTakeover: true},
		},
		{
			name:          "an offer that does not allow the response has it, so it is not kept",
			offers:        []string{"permessage-deflate; client_no_context_takeover; server_max_window_bits=10, permessage-deflate"},
			server_config: PermessageDeflateConfig{ServerMaxWindowBits: 12},
			want:          PermessageDeflateConfig{ServerMaxWindowBits: 12},
		},
		{
			// RFC 7692 7: an offer that does not parse does not count.
			name:          "an offer that does not parse has it, so it is not kept",
			offers:        []string{"permessage-deflate; client_no_context_takeover; x_unknown, permessage-deflate"},
			server_config: PermessageDeflateConfig{},
			want:          PermessageDeflateConfig{},
		},
	}
	for _, testCase := range promises {
		t.Run("client_no_context_takeover: "+testCase.name, func(t *testing.T) {
			request := httptestRequest(t)
			for _, line := range testCase.offers {
				request.Header.Add("Sec-WebSocket-Extensions", line)
			}

			agreed_config, err := validateRespDeflateExtWithReq(testCase.server_config, request)
			if err != nil {
				t.Fatalf("validateRespDeflateExtWithReq: %v", err)
			}
			if agreed_config != testCase.want {
				t.Errorf("agreed_config = %+v, want %+v", agreed_config, testCase.want)
			}
		})
	}

	fails := []struct {
		name          string
		offers        []string
		server_config PermessageDeflateConfig
	}{
		{
			name:          "nothing offered",
			offers:        nil,
			server_config: PermessageDeflateConfig{},
		},
		{
			name:          "only another extension offered",
			offers:        []string{"x-other"},
			server_config: PermessageDeflateConfig{},
		},
		{
			name:          "the only offer does not parse",
			offers:        []string{"permessage-deflate; unknown_param"},
			server_config: PermessageDeflateConfig{},
		},
		{
			// RFC 7692 7.1.2.2.
			name:          "client_max_window_bits, not offered",
			offers:        []string{"permessage-deflate"},
			server_config: PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 10},
		},
		{
			name:          "client_max_window_bits over the offer's",
			offers:        []string{"permessage-deflate; client_max_window_bits=10"},
			server_config: PermessageDeflateConfig{ClientMaxWindowBitsEnabled: true, ClientMaxWindowBits: 12},
		},
		{
			// RFC 7692 7.1.2.1.
			name:          "server_max_window_bits over the offer's",
			offers:        []string{"permessage-deflate; server_max_window_bits=10"},
			server_config: PermessageDeflateConfig{ServerMaxWindowBits: 12},
		},
		{
			// RFC 7692 7.1.1.1.
			name:          "server_no_context_takeover offered, left out of the response",
			offers:        []string{"permessage-deflate; server_no_context_takeover"},
			server_config: PermessageDeflateConfig{},
		},
		{
			// RFC 7692 7.1.2.1.
			name:          "server_max_window_bits offered, left out of the response",
			offers:        []string{"permessage-deflate; server_max_window_bits=10"},
			server_config: PermessageDeflateConfig{},
		},
		{
			// RFC 7692 5: each offer is matched alone, not all of them together.
			name:   "each offer allows one param but none allows both",
			offers: []string{"permessage-deflate; client_max_window_bits; server_max_window_bits=10, permessage-deflate; server_max_window_bits=15"},
			server_config: PermessageDeflateConfig{
				ClientMaxWindowBitsEnabled: true,
				ClientMaxWindowBits:        12,
				ServerMaxWindowBits:        12,
			},
		},
	}
	for _, testCase := range fails {
		t.Run("fails: "+testCase.name, func(t *testing.T) {
			request := httptestRequest(t)
			for _, line := range testCase.offers {
				request.Header.Add("Sec-WebSocket-Extensions", line)
			}

			agreed_config, err := validateRespDeflateExtWithReq(testCase.server_config, request)
			if !errors.Is(err, ErrHandshakeResponseExtensionNotOffered) {
				t.Errorf("err = %v, want %v", err, ErrHandshakeResponseExtensionNotOffered)
			}
			if agreed_config != (PermessageDeflateConfig{}) {
				t.Errorf("agreed_config = %+v, want the zero value", agreed_config)
			}
		})
	}
}

func TestGenerateWebSocketKey(t *testing.T) {
	t.Run("encodes the injected random bytes", func(t *testing.T) {
		got := generateWebSocketKey(generateWebSocketKeyDI{randRead: fixedRandRead(0)})
		want := base64.StdEncoding.EncodeToString(make([]byte, 16))
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("asks for the 16 bytes RFC 6455 requires", func(t *testing.T) {
		var size int
		generateWebSocketKey(generateWebSocketKeyDI{
			randRead: func(b []byte) (int, error) {
				size = len(b)
				return len(b), nil
			},
		})
		if size != 16 {
			t.Errorf("requested %d bytes, want 16", size)
		}
	})

	t.Run("real wiring is base64 and varies between calls", func(t *testing.T) {
		firstKey, secondKey := GenerateWebSocketKey(), GenerateWebSocketKey()
		if firstKey == secondKey {
			t.Error("two keys should not be identical")
		}
		for _, headerKey := range []string{firstKey, secondKey} {
			raw, err := base64.StdEncoding.DecodeString(headerKey)
			if err != nil {
				t.Fatalf("key %q is not base64: %v", headerKey, err)
			}
			if len(raw) != 16 {
				t.Errorf("decoded key is %d bytes, want 16", len(raw))
			}
		}
	})
}

func TestRequestToPlainHTTPMsg(t *testing.T) {
	t.Run("writes the request line and headers", func(t *testing.T) {
		request, err := http.NewRequest("GET", "ws://localhost:8001/", nil)
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}
		request.Header.Set("Sec-WebSocket-Version", "13")

		got, err := requestToPlainHTTPMsg(request, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
		if err != nil {
			t.Fatalf("requestToPlainHTTPMsg: %v", err)
		}
		if !strings.HasPrefix(got, "GET / HTTP/1.1\r\n") {
			t.Errorf("request line wrong, got %q", got)
		}
		if !strings.Contains(got, "Sec-Websocket-Version: 13\r\n") {
			t.Errorf("missing version header in %q", got)
		}
		if !strings.HasSuffix(got, "\r\n\r\n") {
			t.Errorf("headers should end with a blank line, got %q", got)
		}
	})

	/*
		The request-target must be the origin form. Emitting request.URL instead
		would put "GET ws://host:port HTTP/1.1" on the wire, which servers read
		as a proxy request; gin answers an empty path with a 301 rather than
		upgrading, so "ws://localhost:8001" with no trailing slash never
		completes a handshake.
	*/
	t.Run("writes the origin form request target", func(t *testing.T) {
		tests := []struct {
			rawURL string
			want   string
		}{
			{"ws://localhost:8001", "GET / HTTP/1.1\r\n"},
			{"ws://localhost:8001/", "GET / HTTP/1.1\r\n"},
			{"ws://localhost:8001/chat", "GET /chat HTTP/1.1\r\n"},
			{"ws://localhost:8001/chat?room=1", "GET /chat?room=1 HTTP/1.1\r\n"},
			{"wss://localhost:8001/deep/path", "GET /deep/path HTTP/1.1\r\n"},
		}
		for _, testCase := range tests {
			t.Run(testCase.rawURL, func(t *testing.T) {
				request, err := http.NewRequest("GET", testCase.rawURL, nil)
				if err != nil {
					t.Fatalf("http.NewRequest: %v", err)
				}
				got, err := requestToPlainHTTPMsg(request, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
				if err != nil {
					t.Fatalf("requestToPlainHTTPMsg: %v", err)
				}
				if !strings.HasPrefix(got, testCase.want) {
					t.Errorf("request line = %q, want prefix %q", got, testCase.want)
				}
				// The absolute form must not leak into the request line.
				if strings.HasPrefix(got, "GET ws") || strings.HasPrefix(got, "GET http") {
					t.Errorf("absolute form leaked into the request line: %q", got)
				}
			})
		}
	})

	t.Run("fills in Host from the url when absent", func(t *testing.T) {
		request, err := http.NewRequest("GET", "ws://localhost:8001/", nil)
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}
		got, err := requestToPlainHTTPMsg(request, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
		if err != nil {
			t.Fatalf("requestToPlainHTTPMsg: %v", err)
		}
		if !strings.Contains(got, "Host: localhost:8001\r\n") {
			t.Errorf("Host header not derived from the url: %q", got)
		}
	})

	t.Run("keeps an explicit Host header", func(t *testing.T) {
		request, err := http.NewRequest("GET", "ws://localhost:8001/", nil)
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}
		request.Header.Set("Host", "example.com")
		got, err := requestToPlainHTTPMsg(request, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
		if err != nil {
			t.Fatalf("requestToPlainHTTPMsg: %v", err)
		}
		if !strings.Contains(got, "Host: example.com\r\n") {
			t.Errorf("explicit Host was overwritten: %q", got)
		}
	})

	t.Run("appends the body", func(t *testing.T) {
		request, err := http.NewRequest("POST", "ws://localhost:8001/", strings.NewReader("payload"))
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}
		got, err := requestToPlainHTTPMsg(request, requestToPlainHTTPMsgDI{ioReadAll: io.ReadAll})
		if err != nil {
			t.Fatalf("requestToPlainHTTPMsg: %v", err)
		}
		if !strings.HasSuffix(got, "\r\n\r\npayload") {
			t.Errorf("body not appended: %q", got)
		}
	})

	t.Run("propagates a body read error", func(t *testing.T) {
		want := errors.New("body exploded")
		request, err := http.NewRequest("POST", "ws://localhost:8001/", strings.NewReader("x"))
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}
		got, err := requestToPlainHTTPMsg(request, requestToPlainHTTPMsgDI{
			ioReadAll: func(io.Reader) ([]byte, error) { return nil, want },
		})
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
		if got != "" {
			t.Errorf("result should be empty on error, got %q", got)
		}
	})
}
