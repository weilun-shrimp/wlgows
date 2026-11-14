package example_helpers

import "fmt"

// ServerConfig is what the server wizard asked for.
type ServerConfig struct {
	// The certificate and key files. Both are "" when the server runs without
	// TLS.
	ServerCertPath string
	ServerKeyPath  string

	// The subprotocols the server speaks, for PickSubprotocol.
	SupportedSubprotocols []string
}

// ReadUserInputServerConfig runs the server wizard: TLS, see
// ReadUserInputServerTlsInfo, then the subprotocols the server speaks.
func ReadUserInputServerConfig() (ServerConfig, error) {
	var serverConfig ServerConfig
	var err error

	serverConfig.ServerCertPath, serverConfig.ServerKeyPath, err = ReadUserInputServerTlsInfo()
	if err != nil {
		return serverConfig, fmt.Errorf("reading tls info: %w", err)
	}

	serverConfig.SupportedSubprotocols, err = ReadUserInputSubprotocols("Please input the subprotocols the server speaks")
	if err != nil {
		return serverConfig, fmt.Errorf("reading subprotocols: %w", err)
	}

	return serverConfig, nil
}
