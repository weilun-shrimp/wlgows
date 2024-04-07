package connection

import (
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/weilun-shrimp/wlgows/frame"
	"github.com/weilun-shrimp/wlgows/msg"
)

type Conn struct {
	TCP_connection net.Conn
	Client_header  map[string]string // hand shake client header
}

func (this *Conn) HandShake() error {
	client_header, err := decodeClientHandShakeHeader(this.TCP_connection)
	// fmt.Printf("%+v\n", client_header)
	// for i, v := range client_header {
	// 	fmt.Printf("%+v => %+v\n", i, v)
	// }
	if err != nil {
		fmt.Printf("%+v\n", "Error decode Client HandShake Header "+err.Error())
		return err
	}
	this.Client_header = client_header
	hasher := sha1.New()
	io.WriteString(hasher, client_header["Sec-WebSocket-Key"]+"258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
	hashed_key := base64.StdEncoding.EncodeToString((hasher.Sum(nil)))
	return_header := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + hashed_key + "\r\n"
	if client_header["Sec-WebSocket-Protocol"] != "" { // Protocol 可以自己定義，自己做溝通用
		return_header += "Sec-WebSocket-Protocol: " + client_header["Sec-WebSocket-Protocol"] + "\r\n"
	}
	return_header += "\r\n"
	// fmt.Println("Hand Shake server header =>")
	// fmt.Printf("%+v\n", return_header)
	_, err = this.TCP_connection.Write([]byte(return_header))
	if err != nil {
		fmt.Println("Error sending WebSocket handshake response:", err)
		return err
	}
	return nil
}

func decodeClientHandShakeHeader(conn net.Conn) (map[string]string, error) {
	buffer := make([]byte, 600) // chrome header大概會有518個字，所以設600
	var client_header map[string]string = make(map[string]string)
	read_len, err := conn.Read(buffer)
	if err != nil {
		return client_header, err
	}
	if read_len == 0 {
		return client_header, errors.New("empty header")
	}

	splited := strings.Split(string(buffer), "\r\n")
	method_and_protocol_split := strings.Split(splited[0], "/")
	if len(method_and_protocol_split) < 3 {
		return client_header, errors.New("method or protocol is invalid")
	}
	client_header["Method"] = strings.TrimSpace(method_and_protocol_split[0])
	client_header["Protocol"] = strings.TrimSpace(method_and_protocol_split[1])
	client_header["Protocol-Version"] = strings.TrimSpace(method_and_protocol_split[2])

	for _, v := range splited[1:] {
		splited_v := strings.Split(v, ":")
		// k := strings.TrimSpace(splited_v[0])
		k := strings.Trim(splited_v[0], "\f\t\r\n\000 ") // \000是ASSIC碼16進制空白但不為空的表示符，如果用TrimSpace不會把\000削掉，所以要用Trim手動指定，這是golang獨有的問題
		if k != "" {
			client_header[k] = strings.TrimSpace(strings.Join(splited_v[1:], ":"))
		}
	}
	// start check request header is valid
	if client_header["Sec-WebSocket-Key"] == "" {
		return client_header, errors.New("Sec-WebSocket-Key is not set correctly in client handshake header for websocket")
	} else if client_header["Connection"] != "Upgrade" {
		return client_header, errors.New("Connection is not set \"Upgrade\" in client handshake header for websocket")
	} else if client_header["Upgrade"] != "websocket" {
		return client_header, errors.New("Upgrade is not set \"websocket\" in client handshake header for websocket")
	}
	// 13 is option for your app, but in general, we set it in 13
	// if client_header["Sec-WebSocket-Version"] != "13" {
	// 	return client_header, errors.New("Sec-WebSocket-Version is not set \"13\" in client handshake header for websocket")
	// }

	return client_header, nil
}

func (this *Conn) Close() {
	this.TCP_connection.Close()
}

func (this *Conn) GetNextFrame() (*frame.Frame, error) {
	frame, err := frame.GetFrameFromTCPConn(this.TCP_connection)
	return frame, err
}

func (this *Conn) GetNextMsg() (msg.Msg, error) {
	msg, err := msg.GetMsgFromTCPConn(this.TCP_connection)
	if err != nil {
		fmt.Printf("%+v\n", "GetNextMsg Error")
	}
	return msg, err
}

func (this *Conn) SendUnMaskedTextMsg(text string) error {
	msg, err := msg.GenUnMaskedTextMsg([]byte(text))
	if err != nil {
		return err
	}
	for _, f := range msg.Frames {
		// net TCP conn 方法
		_, err := this.TCP_connection.Write(f.Seal())
		if err != nil {
			fmt.Println("Error writing:", err.Error())
			return err
		}

		// io 方法
		// _, err := io.WriteString(this.TCP_connection, string(f.Seal()))
		// if err != nil {
		// 	fmt.Println("Error writing:", err.Error())
		// 	return err
		// }
	}
	return nil
}
