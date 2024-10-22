package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"os"

	"github.com/weilun-shrimp/wlgows/client"
	"github.com/weilun-shrimp/wlgows/example_helpers"
)

func main() {
	fmt.Print("Please input the url (eg. ws://localhost:8001) :")
	url, err := example_helpers.ReadUserInput()
	if err != nil {
		fmt.Println("Error reading input url from start: ", err)
		return
	}

	fmt.Print("Please input the trust ca.crt path (eg. ./ca.crt), empty means no need :")
	ca_path, err := example_helpers.ReadUserInput()
	if err != nil {
		fmt.Println("Error reading input ca path from start: ", err)
		return
	}
	var tls_config *tls.Config
	if ca_path != "" {
		tls_config = loadCA(ca_path)
	}

	conn, err := client.Dial(url, tls_config)
	if err != nil {
		fmt.Println("error of dial")
		fmt.Printf("%+v\n", err)
		return
	}

	defer conn.Close()
	if err := conn.HandShake(); err != nil {
		fmt.Println("error of handshake")
		fmt.Printf("%+v\n", err)
		return
	}

	fmt.Println("Client with server handshaked")

	stopChan := make(chan bool, 1)
	go func() { // read the server msg process
	serverReaderLoop:
		for {
			select {
			case <-stopChan:
				fmt.Println("read from server detect the stop sign.")
				stopChan <- true
				break serverReaderLoop
			default:
			}
			msg, err := conn.GetNextMsg()
			if err != nil {
				fmt.Println("Error reading server msg:", err)
				if err == io.EOF {
					fmt.Println("read from server detect the server closed error")
					stopChan <- true
					break serverReaderLoop
				}
			}
			str_msg := msg.GetStr()
			fmt.Println("echo server return: ", str_msg)
		}
	}()

	go func() {
	clientReaderLoop:
		for { // read the client input process
			select {
			case <-stopChan:
				fmt.Println("read from client detect the stop sign.")
				stopChan <- true
				break clientReaderLoop
			default:
			}

			// Read the input until a newline
			input, err := example_helpers.ReadUserInput()
			if err != nil {
				fmt.Println("Error reading input: ", err)
				continue
			}
			if input == "exit" {
				fmt.Println("Client reader bye.")
				stopChan <- true
				return
			}
			conn.SendText([]byte(input))
		}
	}()

innerLoop:
	for {
		select {
		case <-stopChan:
			fmt.Println("main process detect the stop sign. Bye.")
			break innerLoop
		default:
			continue
		}
	}

	close(stopChan)
}

func loadCA(ca_path string) *tls.Config {
	// Load the CA certificate
	caCert, err := os.ReadFile(ca_path)
	if err != nil {
		panic(err)
	}

	// Create a new CertPool and add the CA certificate
	caCertPool := x509.NewCertPool()
	if ok := caCertPool.AppendCertsFromPEM(caCert); !ok {
		panic("failed to append CA certificate")
	}

	return &tls.Config{
		RootCAs: caCertPool,
	}
}
