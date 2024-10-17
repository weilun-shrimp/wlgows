package main

import (
	"fmt"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/weilun-shrimp/wlgows/connection"
)

func main() {
	http.HandleFunc("/", handler) // Register the handler for the root path

	fmt.Println("Starting server on :8080")
	err := http.ListenAndServe(":8080", nil) // Start the server on port 8080
	if err != nil {
		fmt.Println("Error starting server:", err)
	}
}

func handler(w http.ResponseWriter, r *http.Request) {
	serverConn, err := connection.HijackFromHttp(w, r)
	if err != nil {
		http.Error(w, "could not hijack connection", http.StatusInternalServerError)
		return
	}
	defer serverConn.Close()
	_, err = serverConn.HandShake()
	if err != nil {
		fmt.Println("fail to handshake with client")
		fmt.Println(err)
		return
	}
	fmt.Println(serverConn.ClientRequest)
	fmt.Println(serverConn.ServerResponse)

	for {
		msg, err := serverConn.GetNextMsg()
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
		fmt.Printf("%+v\n", "str len: "+strconv.Itoa(len(str)))
		fmt.Printf("%+v\n", "utf-8 len: "+strconv.Itoa(utf8.RuneCountInString(str)))
		fmt.Printf("%+v\n", "msg: "+msg.GetStr())
		serverConn.SendText([]byte(str))
	}
}
