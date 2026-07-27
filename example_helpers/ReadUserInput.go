package example_helpers

import (
	"bufio"
	"os"
	"strings"
)

/*
One reader for the whole process. bufio reads ahead, so building a new
bufio.Reader per call throws away whatever it buffered past the first newline:
interactive typing survives that, but redirected or piped stdin loses
everything after line one and the next call sees EOF.
*/
var stdinReader = bufio.NewReader(os.Stdin)

func ReadUserInput() (string, error) {
	// Read the input until a newline
	result, err := stdinReader.ReadString('\n')
	if err != nil {
		return result, err
	}
	// TrimRight rather than replacing "\n" alone, so windows CRLF input does
	// not leave a trailing "\r" on the value.
	return strings.TrimRight(result, "\r\n"), nil
}
