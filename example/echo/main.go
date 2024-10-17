package main

import (
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/weilun-shrimp/wlgows/connection"
	"github.com/weilun-shrimp/wlgows/server"
)

func main() {
	service := ":8001"
	s, err := server.Run(service)
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

func handleClient(c *connection.ServerConn) {
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
	fmt.Printf("%+v\n", c.Conn.ClientRequest)
	fmt.Printf("\n")
	fmt.Printf("%+v\n", c.Conn.ServerResponse)

	for {
		msg, err := c.GetNextMsg()
		if err != nil {
			fmt.Printf("%+v\n", err.Error())
			break
		}
		if msg.Frames[0].Opcode == 8 { // disconnect by client
			fmt.Printf("%+v\n", "Disconnect by cleint")
			fmt.Printf("%+v\n", "f:"+strconv.Itoa(len(msg.Frames)))
			fmt.Printf("%+v\n", *msg.Frames[0])
			break
		}
		str := msg.GetStr()
		fmt.Printf("%+v\n", "f:"+strconv.Itoa(len(msg.Frames)))
		fmt.Printf("%+v\n", "str len:"+strconv.Itoa(len(str)))
		fmt.Printf("%+v\n", "utf-8 len:"+strconv.Itoa(utf8.RuneCountInString(str)))
		fmt.Printf("%+v\n", "msg: "+msg.GetStr())
		c.SendText([]byte(str))
	}
}
