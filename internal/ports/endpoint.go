package ports

import (
	"errors"
	"net/url"
	"strings"
)

// Endpoint requires an explicit scheme so plaintext development connections
// cannot be selected accidentally. Credentials are never accepted in URLs.
func Endpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" ||
		strings.ContainsAny(raw, "\\\r\n\t ") {
		return nil, errors.New("invalid service endpoint: use an explicit http:// or https:// URL")
	}
	return u, nil
}
