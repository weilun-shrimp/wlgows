package example_helpers

import "fmt"

// the certFile should be the concatenation of the server's certificate, any intermediates, and the CA's certificate.
func LoadServerTlsInfo() (string, string, error) {
	var server_crt_path string
	var server_key_path string

	fmt.Print("Please input the server.crt path (eg. ./server_fullchain.crt), empty means no need :")
	server_crt_path, err := ReadUserInput()
	if err != nil {
		fmt.Println("Error reading input server crt path from start: ", err)
		return server_crt_path, server_key_path, err
	}
	if server_crt_path == "" {
		return server_crt_path, server_key_path, nil
	}

	fmt.Print("Please input the server.key path (eg. ./server.key), empty means no need :")
	server_key_path, err = ReadUserInput()
	if err != nil {
		fmt.Println("Error reading input server key path from start: ", err)
		return server_crt_path, server_key_path, err
	}

	return server_crt_path, server_key_path, nil
}
