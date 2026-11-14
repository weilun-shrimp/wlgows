package example_helpers

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

const (
	// The bundled test files in example/, one level up from the server's folder.
	defaultServerCertPath = "../server_fullchain.crt"
	defaultServerKeyPath  = "../server.key"
)

// ReadUserInputServerTlsInfo asks whether to serve TLS and, on yes, reads the
// cert and key paths. An empty answer uses ../server_fullchain.crt and
// ../server.key. No TLS returns two empty paths. The cert file holds the
// server's certificate, then any intermediates, then the CA's certificate.
func ReadUserInputServerTlsInfo() (serverCertPath string, serverKeyPath string, err error) {
	useTls, err := ReadUserInputYesNo("Use TLS?")
	if err != nil || !useTls {
		return "", "", err
	}

	fmt.Print("Please input the server cert path, empty means " + defaultServerCertPath + " :")
	serverCertPath, err = ReadUserInput()
	if err != nil {
		return "", "", err
	}
	if serverCertPath == "" {
		serverCertPath = defaultServerCertPath
	}

	fmt.Print("Please input the server key path, empty means " + defaultServerKeyPath + " :")
	serverKeyPath, err = ReadUserInput()
	if err != nil {
		return "", "", err
	}
	if serverKeyPath == "" {
		serverKeyPath = defaultServerKeyPath
	}

	return serverCertPath, serverKeyPath, nil
}

// LoadCA reads the CA certificate at caPath and returns a TLS config that
// trusts it. It panics when the file cannot be read or holds no certificate.
func LoadCA(caPath string) *tls.Config {
	caCert, err := os.ReadFile(caPath)
	if err != nil {
		panic(err)
	}

	caCertPool := x509.NewCertPool()
	if ok := caCertPool.AppendCertsFromPEM(caCert); !ok {
		panic("failed to append CA certificate")
	}

	return &tls.Config{RootCAs: caCertPool}
}
