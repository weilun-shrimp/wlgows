package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/weilun-shrimp/wlgows/connection"
)

func main() {
	r := gin.Default()
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})
	r.GET("/", handler)
	r.Run() // listen and serve on 0.0.0.0:8080 (for windows "localhost:8080")
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
