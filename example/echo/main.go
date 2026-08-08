package main

import (
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/weilun-shrimp/wlgows/v3"
	"github.com/weilun-shrimp/wlgows/v3/example_helpers"
)

// Refused at the frame header before anything is allocated. 0 would mean no limit.
const maxFrameByteLength = 10 << 20 // 10 MB

func main() {
	service := ":8001"
	s, err := wlgows.Run(service)
	if err != nil {
		fmt.Println("Error server run : " + err.Error())
		return
	}
	defer s.Close()

	fmt.Println("Server is listening on port " + service)
	for {
		conn, err := s.Accept()
		if err != nil {
			fmt.Println("Error listener accept : " + err.Error())
			continue
		}
		fmt.Println("New listener accept")

		go handleClient(conn)
	}
}

func handleClient(c *wlgows.ServerConn) {
	// c.SetReadDeadline(time.Now().Add(10 * time.Second)) // set 2 minutes timeout
	// c.SetKeepAlive(true)
	// c.SetKeepAlivePeriod(5 * time.Second)
	// c.SetLinger(3)
	defer c.Close() // close connection before exit

	_, err := c.HandShake()
	if err != nil {
		fmt.Println("Error on handshake")
		fmt.Println(err)
		return
	}
	fmt.Printf("%+v\n", c.ClientRequest)
	fmt.Printf("\n")
	fmt.Printf("%+v\n", c.ServerResponse)

	for {
		msg, err := example_helpers.ReadNextFrames(c, maxFrameByteLength)
		if err != nil {
			fmt.Printf("%+v\n", err.Error())
			break
		}
		if msg[0].Opcode == 8 { // disconnect by client
			fmt.Printf("%+v\n", "Disconnect by cleint")
			fmt.Printf("%+v\n", "f:"+strconv.Itoa(len(msg)))
			fmt.Printf("%+v\n", *msg[0])
			break
		}
		str := msg.String()
		fmt.Printf("%+v\n", "f:"+strconv.Itoa(len(msg)))
		fmt.Printf("%+v\n", "str len:"+strconv.Itoa(len(str)))
		fmt.Printf("%+v\n", "utf-8 len:"+strconv.Itoa(utf8.RuneCountInString(str)))
		fmt.Printf("%+v\n", "msg: "+msg.String())
		c.SendText([]byte(str))
	}
}
