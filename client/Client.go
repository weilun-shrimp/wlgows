package client

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"

	"github.com/weilun-shrimp/wlgows/connection"
)

type Client struct {
	Host           string
	Path           string //
	ServerProtocal string // only accept in (ws, wss).
	Conn           *connection.Conn
}

// params server_protocal only accept in (ws, wss).
func (client *Client) Dial(tls_config *tls.Config) error {
	var conn net.Conn
	var err error

	// set default value
	if client.Host == "" {
		client.Host = "localhost"
	}
	if client.Path == "" {
		client.Path = "/"
	}
	if client.ServerProtocal == "" {
		client.ServerProtocal = "ws"
	}

	switch client.ServerProtocal {
	case "ws":
		conn, err = net.Dial("tcp", client.Host)
	case "wss":
		conn, err = tls.Dial("tcp", client.Host, tls_config)
	default:
		return errors.New("wrong server protocal")
	}

	if err != nil {
		fmt.Println("Error for client dial to server:", err)
		return err
	}

	client.Conn = &connection.Conn{TCP_connection: conn}
	return nil
}

func (client *Client) Close() {
	client.Conn.Close()
}

func (client *Client) HandShake(custom_header map[string]string) error {
	err := client.Conn.ClientHandShake(client.Host, client.Path, custom_header)
	if err != nil {
		return err
	}
	return nil
}
