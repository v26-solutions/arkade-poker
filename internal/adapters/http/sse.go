package http

import (
	"bufio"
	"bytes"
	"errors"
	"io"

	"arkade-poker/go/internal/adapters/indexdata"
)

type sse struct {
	body  io.ReadCloser
	lines *bufio.Scanner
	limit int
	first bool
}

func newSSE(body io.ReadCloser, limit int) *sse {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, min(4096, limit)), limit+1)
	scanner.Split(sseLine)
	return &sse{body: body, lines: scanner, limit: limit, first: true}
}

// SSE accepts LF, CRLF and CR, even when a CRLF straddles network chunks.
func sseLine(data []byte, atEOF bool) (int, []byte, error) {
	for i, b := range data {
		if b == '\n' {
			return i + 1, data[:i], nil
		}
		if b == '\r' {
			if i+1 == len(data) && !atEOF {
				return 0, nil, nil
			}
			n := i + 1
			if n < len(data) && data[n] == '\n' {
				n++
			}
			return n, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func (s *sse) Close() error { return s.body.Close() }
func (s *sse) Recv() (indexdata.SubscriptionFrame, error) {
	var data []byte
	var kind string
	size := 0
	hasData := false
	for s.lines.Scan() {
		line := s.lines.Bytes()
		size += len(line) + 1
		if size > s.limit {
			return indexdata.SubscriptionFrame{}, errors.New("subscription frame exceeds byte limit")
		}
		if s.first {
			line = bytes.TrimPrefix(line, []byte{0xef, 0xbb, 0xbf})
			s.first = false
		}
		if len(line) == 0 {
			if hasData {
				if kind != "" && kind != "message" {
					return indexdata.SubscriptionFrame{}, errors.New("subscription stream error or unsupported event")
				}
				return decodeSubscription(data)
			}
			size = 0
			kind = ""
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := bytes.Cut(line, []byte{':'})
		value = bytes.TrimPrefix(value, []byte{' '})
		switch string(field) {
		case "event":
			kind = string(value)
		case "data":
			if hasData {
				data = append(data, '\n')
			}
			data = append(data, value...)
			hasData = true
		}
	}
	if err := s.lines.Err(); err != nil {
		return indexdata.SubscriptionFrame{}, errors.New("subscription stream read failed or exceeded line limit")
	}
	if size > 0 {
		return indexdata.SubscriptionFrame{}, io.ErrUnexpectedEOF
	}
	return indexdata.SubscriptionFrame{}, io.EOF
}
