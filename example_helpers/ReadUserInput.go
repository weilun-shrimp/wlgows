package example_helpers

import (
	"bufio"
	"os"
	"strings"
)

func ReadUserInput() (string, error) {
	// Create a new reader that reads from standard input
	reader := bufio.NewReader(os.Stdin)
	// Read the input until a newline
	result, err := reader.ReadString('\n')
	if err != nil {
		return result, err
	}
	return strings.ReplaceAll(result, "\n", ""), nil
}
