package lsp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Limits of the framing (spec §3.2): the largest Content-Length, the
// longest header line and the most header lines of one message.
const (
	maxBody        = 64 << 20
	maxHeaderLine  = 64 << 10
	maxHeaderLines = 64
)

// newReader is the reader readMessage needs: its buffer holds the
// longest header line.
func newReader(r io.Reader) *bufio.Reader { return bufio.NewReaderSize(r, maxHeaderLine+2) }

// errFraming is a header block the server cannot read past.
var errFraming = errors.New("framing")

// readMessage reads one framed message body. It returns io.EOF at end of
// input before a message starts, io.ErrUnexpectedEOF inside one, and an
// error wrapping errFraming for a header block without a valid
// Content-Length, with one above maxBody, with a header line longer than
// maxHeaderLine or with more than maxHeaderLines lines. r must come from
// newReader.
func readMessage(r *bufio.Reader) ([]byte, error) {
	length := -1
	started := false
	for lines := 0; ; lines++ {
		if lines > maxHeaderLines {
			return nil, fmt.Errorf("%w: more than %d header lines", errFraming, maxHeaderLines)
		}
		slice, err := r.ReadSlice('\n')
		line := string(slice)
		if err == bufio.ErrBufferFull || len(strings.TrimRight(line, "\r\n")) > maxHeaderLine {
			return nil, fmt.Errorf("%w: a header line is longer than %d bytes", errFraming, maxHeaderLine)
		}
		if err != nil {
			if err == io.EOF && !started && line == "" {
				return nil, io.EOF
			}
			if err == io.EOF {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		started = true
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("%w: header line %q has no colon", errFraming, line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("%w: Content-Length %q is not a length", errFraming, strings.TrimSpace(value))
			}
			if n > maxBody {
				return nil, fmt.Errorf("%w: Content-Length %d is above %d", errFraming, n, maxBody)
			}
			length = n
		}
		// Content-Type and any other header are accepted and ignored
	}
	if length < 0 {
		return nil, fmt.Errorf("%w: no Content-Length", errFraming)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return body, nil
}

// writeMessage frames and writes one message whole.
func writeMessage(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	msg := make([]byte, 0, len(body)+32)
	msg = append(msg, "Content-Length: "...)
	msg = strconv.AppendInt(msg, int64(len(body)), 10)
	msg = append(msg, "\r\n\r\n"...)
	msg = append(msg, body...)
	_, err = w.Write(msg)
	return err
}
