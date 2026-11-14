package example_helpers

import (
	"fmt"
	"slices"
	"strings"
)

// ReadUserInputSubprotocols prints question and reads a comma separated list.
// Spaces around each name and empty entries are dropped, so an empty line is
// none.
// Names are not checked: each must be a token (RFC 6455 4.1), so no spaces or
// separators inside.
func ReadUserInputSubprotocols(question string) ([]string, error) {
	fmt.Print(question + " (eg. chat.v2,chat.v1), empty means none :")
	answer, err := ReadUserInput()
	if err != nil {
		return nil, err
	}
	var subprotocols []string
	for _, subprotocol := range strings.Split(answer, ",") {
		if subprotocol = strings.TrimSpace(subprotocol); subprotocol != "" {
			subprotocols = append(subprotocols, subprotocol)
		}
	}
	return subprotocols, nil
}

// PrintSubprotocol prints the subprotocol the connection uses, or that there
// is none.
func PrintSubprotocol(subprotocol string) {
	if subprotocol == "" {
		fmt.Println("no subprotocol")
		return
	}
	fmt.Println("subprotocol:", subprotocol)
}

// PickSubprotocol returns the client's first offer the server speaks, or "" for
// none. The client's order first is this example's choice: RFC 6455 4.2.2 lets
// the server pick any one the client offered.
func PickSubprotocol(offered []string, supported []string) string {
	for _, subprotocol := range offered {
		if slices.Contains(supported, subprotocol) {
			return subprotocol
		}
	}
	return ""
}
