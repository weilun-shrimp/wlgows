package connection

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"strings"
)

func (c *Conn) ClientHandShake(host string, path string, custom_client_header map[string]string) error {
	c.ClientHeader = map[string]string{
		"Host":                  host,
		"Upgrade":               "websocket",
		"connection":            "Upgrade",
		"Sec-WebSocket-Key":     generateWebSocketKey(),
		"Sec-WebSocket-Version": "13",
	}
	for k, v := range custom_client_header {
		c.ClientHeader[k] = v
	}
	_, err := c.TCP_connection.Write([]byte("GET " + path + " HTTP/1.1\r\n" + headerToStr(c.ClientHeader)))
	if err != nil {
		return err
	}
	return nil
}

func (c *Conn) ClientCheckHandShake() error {
	server_header, err := decodeServerHandShakeHeader(c.TCP_connection)
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

func decodeServerHandShakeHeader(conn net.Conn) (map[string]string, error) {
	buffer := make([]byte, 1024)
	var server_header map[string]string = make(map[string]string)
	read_len, err := conn.Read(buffer)
	if err != nil {
		return server_header, err
	}
	if read_len == 0 {
		return server_header, errors.New("empty header")
	}

	row_string := string(buffer)
	splited := strings.Split(row_string, "\r\n")
	top_split := strings.Split(splited[0], " ")
	if len(top_split) < 2 {
		return server_header, errors.New("top_split is invalid. row_string: " + row_string)
	}
	server_header["Protocol"] = strings.TrimSpace(top_split[0])
	server_header["Status"] = strings.TrimSpace(top_split[1])

	for _, v := range splited[1:] {
		splited_v := strings.Split(v, ":")
		// k := strings.TrimSpace(splited_v[0])
		k := strings.Trim(splited_v[0], "\f\t\r\n\000 ") // \000是ASSIC碼16進制空白但不為空的表示符，如果用TrimSpace不會把\000削掉，所以要用Trim手動指定，這是golang獨有的問題
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
