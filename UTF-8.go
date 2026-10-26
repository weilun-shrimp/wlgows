package wlgows

import (
	"io"
	"unicode/utf8"
)

/*
utf8Checker checks bytes put in pieces as one UTF-8 text (RFC 3629), as a
text message's frames (RFC 6455 8.1). A rune may be split across puts.
*/
type utf8Checker struct {
	invalid bool
	// incompleteRune is a rune cut at the end of the last put, waiting for its
	// next bytes.
	incompleteRune       [utf8.UTFMax]byte
	incompleteRuneLength int
}

// put adds data after the bytes put before. Once invalid, it stays invalid.
func (c *utf8Checker) put(data []byte) {
	if c.invalid {
		return
	}

	// complete the rune cut at the end of the last put
	for c.incompleteRuneLength > 0 && len(data) > 0 {
		c.incompleteRune[c.incompleteRuneLength] = data[0]
		c.incompleteRuneLength++
		data = data[1:]
		rune_bytes := c.incompleteRune[:c.incompleteRuneLength]
		// FullRune is also true once these bytes can't start a valid rune
		if !utf8.FullRune(rune_bytes) {
			continue
		}
		if !utf8.Valid(rune_bytes) {
			c.invalid = true
			return
		}
		c.incompleteRuneLength = 0
	}

	// keep a rune cut at the end of data for the next put. A rune is at most 4
	// bytes, so look back only that far. No rune start there: nothing is kept,
	// and Valid rejects those continuation bytes.
	last_rune_start := len(data)
	for i := len(data) - 1; i >= 0 && i >= len(data)-utf8.UTFMax; i-- {
		if utf8.RuneStart(data[i]) {
			last_rune_start = i
			break
		}
	}
	if last_rune_start < len(data) && !utf8.FullRune(data[last_rune_start:]) {
		c.incompleteRuneLength = copy(c.incompleteRune[:], data[last_rune_start:])
		data = data[:last_rune_start]
	}

	if !utf8.Valid(data) {
		c.invalid = true
	}
}

// isValid is whether the bytes put so far are UTF-8: true when nothing was
// put, false while a rune is cut at the end.
func (c *utf8Checker) isValid() bool {
	return !c.invalid && c.incompleteRuneLength == 0
}

// utf8CheckReader reads reader and checks what it read is UTF-8, as a text
// message must be (RFC 6455 8.1).
type utf8CheckReader struct {
	reader  io.ReadCloser
	checker utf8Checker
}

// Read reads into p. When reader returns io.EOF and the bytes read are not
// UTF-8, or end in a cut rune, Read returns ErrInvalidUTF8 in place of io.EOF.
// Bytes come out before they are checked: trust them only after io.EOF. Other
// errors return as they are.
func (r *utf8CheckReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.checker.put(p[:n])
	if err == io.EOF && !r.checker.isValid() {
		return n, ErrInvalidUTF8
	}
	return n, err
}

// Close closes reader.
func (r *utf8CheckReader) Close() error {
	return r.reader.Close()
}
