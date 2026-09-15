// Package http implements bounded gateway requests using Fetch on js/wasm.
// Gateway DTOs stay here; generated native RPC packages are deliberately absent.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arkade-poker/go/internal/ports"
)

type Client struct {
	base   string
	client *http.Client
}

type HTTPStatusError int

func (e HTTPStatusError) Error() string { return fmt.Sprintf("service HTTP status %d", e) }

func New(endpoint string) (*Client, error) {
	u, err := ports.Endpoint(endpoint)
	if err != nil {
		return nil, err
	}
	return &Client{strings.TrimRight(u.String(), "/"), &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("service redirect refused") },
	}}, nil
}

func (c *Client) request(ctx context.Context, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		if len(data) > 20<<20 {
			return errors.New("service request exceeds byte limit")
		}
		input = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, input)
	if err != nil {
		return err
	}
	// A JSON Content-Type on a bodyless GET triggers a browser CORS preflight.
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body = bindBody(ctx, resp.Body)
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Service diagnostics may contain request data. Do not echo them into UI
		// or durable logs; status identifies failure without exposing PSBTs.
		return HTTPStatusError(resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (20<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 20<<20 {
		return errors.New("service response exceeds byte limit")
	}
	return decodeProtoFields(data, out)
}

// ProtoJSON encodes int64 as decimal strings. Accept integer JSON tokens too,
// with no float conversion, exponent notation, whitespace, or overflow.
type decimal int64

func (n *decimal) UnmarshalJSON(data []byte) error {
	s := string(data)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	if s == "" {
		return errors.New("missing decimal")
	}
	for i, c := range s {
		if (c < '0' || c > '9') && !(i == 0 && c == '-') {
			return errors.New("invalid decimal")
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return errors.New("decimal out of range")
	}
	*n = decimal(v)
	return nil
}

func (c *Client) Close() error { c.client.CloseIdleConnections(); return nil }
