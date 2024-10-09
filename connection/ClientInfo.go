package connection

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"io"
	"strings"
)

type ClientInfo struct {
	Method          string            `json:"Method"`
	Path            string            `json:"Path"`
	Protocol        string            `json:"Protocal"`
	ProtocolVersion string            `json:"ProtocalVersion"`
	Headers         map[string]string `json:"Headers"`
}

// return like "GET / HTTP/1.1\r\n"
func (ci *ClientInfo) GetHttpRequestMsgTop() string {
	return ci.Method + " " + ci.Path + " " + ci.Protocol + "/" + ci.ProtocolVersion + "\r\n"
}

/*
return like
GET / HTTP/1.1\r\n
Content-Type: application/json\r\n
......
*/
func (ci *ClientInfo) GetHttpRequestMsg() string {
	return ci.GetHttpRequestMsgTop() + headerToStr(ci.Headers)
}

func (c *Conn) ClientHandShake(host string, custom_client_header map[string]string) error {
	if c.ClientInfo == nil {
		c.ClientInfo = &ClientInfo{
			Method:          "GET",
			Path:            "/",
			Protocol:        "HTTP",
			ProtocolVersion: "1.1",
			Headers:         make(map[string]string, 0),
		}
	}

	for k, v := range map[string]string{
		"Host":                  host,
		"Upgrade":               "websocket",
		"Connection":            "Upgrade",
		"Sec-WebSocket-Key":     generateWebSocketKey(),
		"Sec-WebSocket-Version": "13",
	} {
		c.ClientInfo.Headers[k] = v
	}
	for k, v := range custom_client_header {
		c.ClientHeader[k] = v
	}

	if _, err := c.Write([]byte(c.ClientInfo.GetHttpRequestMsg())); err != nil {
		return err
	}

	return nil
}

func (c *Conn) ClienShakeServerHand() error {
	server_header, err := decodeServerHandShake()
	if err != nil {
		return err
	}
	c.ServerHeader = server_header
	err = validateServerHeaderFormation(c.ServerHeader)
	if err != nil {
		return err
	}

	// check server Sec-WebSocket-Accept and client Sec-WebSocket-Key is match
	hasher := sha1.New()
	io.WriteString(hasher, c.ClientHeader["Sec-WebSocket-Key"]+"258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
	expectedKey := base64.StdEncoding.EncodeToString((hasher.Sum(nil)))
	if expectedKey != c.ServerHeader["Sec-WebSocket-Accept"] {
		return errors.New("Sec-WebSocket-Accept not valid")
	}

	return nil
}

/*
Server http request msg like
HTTP/1.1 101 Switching Protocols\r\n
Header1: HeaderContent\r\n
Header2: HeaderContent\r\n
*/
func (c *Conn) DecodeServerHandShake() error {
	buffer := make([]byte, 1024)
	if read_len, err := c.TCP_connection.Read(buffer); err != nil || read_len == 0 {
		if err != nil {
			return err
		}
		if read_len == 0 {
			return errors.New("Server handshake http request msg empty.")
		}
	}
	if c.ServerInfo == nil {
		c.ServerInfo = &ServerInfo{
			Headers: make(map[string]string, 0),
		}
	}

	splited := strings.Split(string(buffer), "\r\n")
	// http top
	top_splited := strings.Split(splited[0], " ")
	if len(top_splited) < 3 {
		return errors.New("Server handshake top is invalid.  Raw server handshake top => " + splited[0])
	}
	protocal_split := strings.Split(top_splited[0], "/")
	if len(protocal_split) < 2 {
		return errors.New("Server handshake top protocol formation is invalid. Raw server handshake top => " + splited[0])
	}
	c.ServerInfo.Protocal = strings.TrimSpace(protocal_split[0])
	c.ServerInfo.ProtocalVersion = strings.TrimSpace(protocal_split[1])
	c.ServerInfo.StatusCode = strings.TrimSpace(top_splited[1])
	c.ServerInfo.Description = strings.TrimSpace(strings.Join(top_splited[2:], " "))
	// http header
	for _, v := range splited[1:] {
		v = strings.Trim(v, "\f\t\r\n\000 ")
		if v == "" {
			continue
		}
		splited_v := strings.Split(v, ":")
		if len(splited_v) < 2 {
			return errors.New("Server handshake header formation error. Raw server handshake msg => " + string(buffer))
		}
		// k := strings.TrimSpace(splited_v[0])
		k := strings.Trim(splited_v[0], "\f\t\r\n\000 ") // \000是ASSIC碼16進制空白但不為空的表示符，如果用TrimSpace不會把\000削掉，所以要用Trim手動指定，這是golang獨有的問題
		if k == "" {

		}
		if k != "" {
			server_header[k] = strings.TrimSpace(strings.Join(splited_v[1:], ":"))
		}
	}
	return server_header, nil
}

func validateServerHeaderFormation(server_header map[string]string) error {
	if server_header["Status"] == "101" {
		return errors.New(" Sec-WebSocket-Accept is not set correctly in server handshake header for websocket")
	}
	if server_header["Sec-WebSocket-Accept"] == "" {
		return errors.New(" Sec-WebSocket-Accept is not set correctly in server handshake header for websocket")
	}
	if server_header["Connection"] != "Upgrade" {
		return errors.New(" Connection is not set \"Upgrade\" in server handshake header for websocket")
	}
	if server_header["Upgrade"] != "websocket" {
		return errors.New(" Upgrade is not set \"websocket\" in server handshake header for websocket")
	}
	// 13 is option for your app, but in general, we set it in 13
	// if server_header["Sec-WebSocket-Version"] != "13" {
	// 	return server_header, errors.New("Sec-WebSocket-Version is not set \"13\" in client handshake header for websocket")
	// }
	return nil
}

func generateWebSocketKey() string {
	key := make([]byte, 16)
	rand.Read(key)
	return base64.StdEncoding.EncodeToString(key)
}
