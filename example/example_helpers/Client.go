package example_helpers

import (
	"crypto/tls"
	"fmt"
	"net/url"

	"github.com/weilun-shrimp/wlgows/v7"
)

const (
	// Where the example servers listen.
	defaultWebsocketUrl = "ws://localhost:8001"

	// The bundled test CA in example/, one level up from the client's folder.
	defaultCaPath = "../ca.crt"
)

// ClientConfig is what the client wizard asked for.
type ClientConfig struct {
	// For DialContext.
	WebsocketUrl string

	// For DialContext. nil for ws and http.
	TlsConfig *tls.Config

	// For ClientHandShake: the deflate offers and subprotocols typed in.
	HandShakeConfig wlgows.ClientHandShakeConfig
}

// ReadUserInputClientConfig runs the client wizard: the url, the trust CA
// (https and wss only), permessage-deflate and the subprotocols. Empty answers
// use ws://localhost:8001 and ../ca.crt. It is called before dialing, so the
// dial timeout does not run while you type.
func ReadUserInputClientConfig() (ClientConfig, error) {
	var clientConfig ClientConfig

	fmt.Print("Please input the url, empty means " + defaultWebsocketUrl + " :")
	websocketUrl, err := ReadUserInput()
	if err != nil {
		return clientConfig, fmt.Errorf("reading url: %w", err)
	}
	if websocketUrl == "" {
		websocketUrl = defaultWebsocketUrl
	}
	parsedUrl, err := url.Parse(websocketUrl)
	if err != nil {
		return clientConfig, fmt.Errorf("parsing url: %w", err)
	}
	clientConfig.WebsocketUrl = websocketUrl

	// A trust CA only matters for TLS, which the library dials for https and
	// wss. url.Parse lowercases the scheme.
	if parsedUrl.Scheme == "https" || parsedUrl.Scheme == "wss" {
		fmt.Print("Please input the trust ca.crt path, empty means " + defaultCaPath + " :")
		caPath, err := ReadUserInput()
		if err != nil {
			return clientConfig, fmt.Errorf("reading ca path: %w", err)
		}
		if caPath == "" {
			caPath = defaultCaPath
		}
		clientConfig.TlsConfig = LoadCA(caPath)
	}

	// The mode chosen, or none (RFC 7692 5).
	clientConfig.HandShakeConfig.PermessageDeflates, err = ReadUserInputDeflateOffers()
	if err != nil {
		return clientConfig, fmt.Errorf("reading deflate mode: %w", err)
	}

	// Subprotocols (RFC 6455 4.1), offered in the order typed.
	clientConfig.HandShakeConfig.Subprotocols, err = ReadUserInputSubprotocols("Please input the subprotocols to offer, in order of preference")
	if err != nil {
		return clientConfig, fmt.Errorf("reading subprotocols: %w", err)
	}

	return clientConfig, nil
}
