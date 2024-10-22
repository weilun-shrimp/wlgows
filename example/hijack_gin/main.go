package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/weilun-shrimp/wlgows/connection"
	"github.com/weilun-shrimp/wlgows/example_helpers"
)

func main() {
	server_crt_path, server_key_path, err := example_helpers.LoadServerTlsInfo()
	if err != nil {
		fmt.Println("error in load server tls info")
		panic(err)
	}

	r := gin.Default()
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})
	r.GET("/", handler)

	fmt.Println("listen and serve on 0.0.0.0:8001 (for windows localhost:8001)")
	if server_crt_path != "" && server_key_path != "" {
		fmt.Println("Run on tls mode")
		err = r.RunTLS(":8001", server_crt_path, server_key_path)
	} else {
		fmt.Println("Run on general mode")
		err = r.Run(":8001")
	}
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func handler(c *gin.Context) {
	serverConn, err := connection.HijackFromGin(c)
	if err != nil {
		c.AbortWithError(http.StatusInternalServerError, errors.New("could not hijack connection"))
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
