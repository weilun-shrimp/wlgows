package example_helpers

import (
	"bufio"
	"fmt"
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

// ReadUserInput reads one line from stdin and drops the trailing \r\n. On an
// error it returns what was read so far, untrimmed.
func ReadUserInput() (string, error) {
	// Read the input until a newline
	result, err := stdinReader.ReadString('\n')
	if err != nil {
		return result, err
	}
	// TrimRight rather than replacing "\n" alone, so Windows CRLF input does
	// not leave a trailing "\r" on the value.
	return strings.TrimRight(result, "\r\n"), nil
}

// ReadUserInputYesNo prints question and reads the answer: "y" or "Y" is
// true, anything else is false.
func ReadUserInputYesNo(question string) (bool, error) {
	fmt.Print(question + " (y/Y = yes, other = no) :")
	answer, err := ReadUserInput()
	if err != nil {
		return false, err
	}
	return answer == "y" || answer == "Y", nil
}
