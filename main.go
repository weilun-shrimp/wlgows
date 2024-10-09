package main

import (
	"fmt"
	"net/url"
)

func main() {
	result, err := url.Parse("/path?test=123#fragment")

	if err != nil {
		fmt.Printf("%+v\n", "err")
		fmt.Printf("%+v\n", err)
	} else {
		fmt.Printf("%+v\n", "result")
		fmt.Printf("%+v\n", result.Scheme)
	}
}
