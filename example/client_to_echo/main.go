package main

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/weilun-shrimp/wlgows/client"
)

func main() {

	conn, err := client.Dial("ws://localhost:8001", nil)
	if err != nil {
		fmt.Printf("%+v\n", err)
		return
	}
	defer conn.Close()
	if err := conn.HandShake(); err != nil {
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

			// Create a new reader that reads from standard input
			reader := bufio.NewReader(os.Stdin)

			// Read the input until a newline
			input, err := reader.ReadString('\n')
			if err != nil {
				fmt.Println("Error reading input: ", err)
				continue
			}
			if input == "exit\n" {
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
			// stopChan <- true
			break innerLoop
		default:
			continue
		}
	}

	close(stopChan)
}
