package connection

import (
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

type ServerInfo struct {
	Protocal        string            `json:"Protocal"`
	ProtocalVersion string            `json:"ProtocalVersion"`
	StatusCode      string            `json:"StatusCode"`
	Description     string            `json:"Description"`
	Headers         map[string]string `json:"Headers"`
}

// return like "HTTP/1.1 101 Switching Protocols\r\n"
func (s *ServerInfo) GetHttpRequestMsgTop() string {
	return s.Protocal + "/" + s.ProtocalVersion + " " + s.StatusCode + " " + s.Description + "\r\n"
}

/*
return like
HTTP/1.1 101 Switching Protocols\r\n
Content-Type: application/json\r\n
......
*/
func (s *ServerInfo) GetHttpRequestMsg() string {
	return s.GetHttpRequestMsgTop() + headerToStr(s.Headers)
}

func (c *Conn) ServerHandShake(custom_server_header map[string]string) error {
	if err := c.DecodeClientHandShake(); err != nil {
		fmt.Printf("%+v\n", "Error decode Client HandShake. "+err.Error())
		return err
	}
	if err := c.ValidateClientHandShake(); err != nil {
		fmt.Printf("%+v\n", "Error validate Client HandShake. "+err.Error())
		return err
	}

	if c.ServerInfo == nil {
		c.ServerInfo = &ServerInfo{
			Protocal:        "HTTP",
			ProtocalVersion: "1.1",
			StatusCode:      "101",
			Description:     "Switching Protocols",
			Headers:         make(map[string]string, 0),
		}
	}
	for k, v := range map[string]string{
		"Upgrade":               "websocket",
		"Connection":            "Upgrade",
		"Sec-WebSocket-Version": "13",
		"Sec-WebSocket-Accept":  generateSecWebsocketAccept(c.ClientInfo.Headers["Sec-WebSocket-Key"]),
	} {
		c.ServerInfo.Headers[k] = v
	}
	if val, ok := c.ClientInfo.Headers["Sec-WebSocket-Protocol"]; ok && val != "" { // Protocol 可以自己定義，自己做溝通用
		c.ServerInfo.Headers["Sec-WebSocket-Protocol"] = val
	}
	for k, v := range custom_server_header {
		c.ServerInfo.Headers[k] = v
	}

	return_msg := c.ServerInfo.GetHttpRequestMsgTop() + headerToStr(c.ServerInfo.Headers)
	// fmt.Println("Hand Shake server info =>")
	// fmt.Printf("%+v\n", return_msg)
	_, err := c.Write([]byte(return_msg))
	if err != nil {
		fmt.Println("Error sending WebSocket handshake response:", err)
		return err
	}
	return nil
}

/*
Client http request msg like
GET /path HTTP/1.1\r\n
Header1: HeaderContent\r\n
Header2: HeaderContent\r\n
*/
func (c *Conn) DecodeClientHandShake() error {
	buffer := make([]byte, 1024) // chrome header大概會有518個字
	read_len, err := c.Read(buffer)
	if err != nil {
		return err
	}
	if read_len == 0 {
		return errors.New("Client handshake http request msg empty.")
	}

	if c.ClientInfo == nil { // init client info
		c.ClientInfo = &ClientInfo{
			Headers: make(map[string]string, 0),
		}
	}

	splited := strings.Split(string(buffer), "\r\n")
	top_splited := strings.Split(splited[0], " ")
	if len(top_splited) < 3 {
		return errors.New("Client handshake  top is invalid.  Raw client handshake top => " + splited[0])
	}
	c.ClientInfo.Method = strings.TrimSpace(top_splited[0])
	c.ClientInfo.Path = strings.TrimSpace(top_splited[1])
	protocol_split := strings.Split(top_splited[2], "/")
	if len(protocol_split) < 2 {
		return errors.New("Client handshake protocol formation error. Raw client handshake top => " + splited[0])
	}
	c.ClientInfo.Protocol = strings.TrimSpace(protocol_split[0])
	c.ClientInfo.ProtocolVersion = strings.TrimSpace(protocol_split[1])

	for _, v := range splited[1:] {
		splited_v := strings.Split(v, ":")
		if len(splited_v) < 2 {
			return errors.New("Client handshake header formation error. Raw client handshake msg => " + string(buffer))
		}
		// k := strings.TrimSpace(splited_v[0])
		k := strings.Trim(splited_v[0], "\f\t\r\n\000 ") // \000是ASSIC碼16進制空白但不為空的表示符，如果用TrimSpace不會把\000削掉，所以要用Trim手動指定，這是golang獨有的問題
		if k != "" {
			c.ClientInfo.Headers[k] = strings.TrimSpace(strings.Join(splited_v[1:], ":"))
		}
	}
	return nil
}

func (c *Conn) ValidateClientHandShake() error {
	// top validation
	if c.ClientInfo.Method != "GET" {
		return errors.New("Client handshake method not equal to 'GET'. Raw client handshake method => " + c.ClientInfo.Method)
	}
	if c.ClientInfo.Protocol != "HTTP" {
		return errors.New("Client handshake protocol not equal to 'HTTP'. Raw client handshake protocol => " + c.ClientInfo.Protocol)
	}
	if c.ClientInfo.ProtocolVersion != "1.1" {
		return errors.New("Client handshake protocol version not equal to '1.1'. Raw client handshake protocol version => " + c.ClientInfo.ProtocolVersion)
	}
	// header validation
	if val, ok := c.ClientInfo.Headers["Sec-WebSocket-Key"]; !ok || val == "" {
		return errors.New("Client handshake header Sec-WebSocket-Key is not set correctly for websocket.")
	}
	if val, ok := c.ClientInfo.Headers["Connection"]; !ok || val != "Upgrade" {
		return errors.New("Client handshake header Connection is not set 'Upgrade' for websocket.")
	}
	if val, ok := c.ClientInfo.Headers["Upgrade"]; !ok || val != "websocket" {
		return errors.New("Client handshake header Upgrade is not set 'websocket' for websocket.")
	}
	// 13 is option for your app, but in general, we set it in 13
	// if client_header["Sec-WebSocket-Version"] != "13" {
	// 	return client_header, errors.New("Sec-WebSocket-Version is not set \"13\" in client handshake header for websocket")
	// }
	return nil
}

func generateSecWebsocketAccept(sec_websocket_key string) string {
	hasher := sha1.New()
	io.WriteString(hasher, sec_websocket_key+"258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
	return base64.StdEncoding.EncodeToString((hasher.Sum(nil)))
}
