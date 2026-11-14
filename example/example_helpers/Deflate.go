package example_helpers

import (
	"fmt"

	"github.com/weilun-shrimp/wlgows/v7"
)

// ReadUserInputDeflateOffers asks which permessage-deflate mode to offer
// (RFC 7692). Any answer but y or Y sends no offer, and the connection opens
// without compression.
func ReadUserInputDeflateOffers() ([]wlgows.PermessageDeflateConfig, error) {
	deflate, err := ReadUserInputYesNo("Use permessage-deflate?")
	if err != nil || !deflate {
		return nil, err
	}
	// RFC 7692 7.1.1.2: tells the server the client resets its window after
	// each message.
	clientNoContextTakeover, err := ReadUserInputYesNo("Client no context takeover?")
	if err != nil {
		return nil, err
	}
	// RFC 7692 7.1.1.1: asks the server to reset its window after each message.
	serverNoContextTakeover, err := ReadUserInputYesNo("Server no context takeover?")
	if err != nil {
		return nil, err
	}
	return []wlgows.PermessageDeflateConfig{{
		ClientNoContextTakeover: clientNoContextTakeover,
		ServerNoContextTakeover: serverNoContextTakeover,
	}}, nil
}

// PrintAgreedDeflate prints the permessage-deflate config the handshake agreed,
// or that there is none.
func PrintAgreedDeflate(conn *wlgows.Conn) {
	agreed := conn.GetPermessageDeflateAgreedConfig()
	if agreed == nil {
		fmt.Println("permessage-deflate: not agreed")
		return
	}
	fmt.Println("agreed:", agreed.Format())
}
