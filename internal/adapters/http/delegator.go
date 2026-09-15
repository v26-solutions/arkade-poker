package http

import (
	"context"
	"errors"

	"arkade-poker/go/internal/ports"
)

type Delegator struct{ *Client }

func NewDelegator(endpoint string) (*Delegator, error) {
	c, err := New(endpoint)
	if err != nil {
		return nil, err
	}
	return &Delegator{c}, nil
}

func (d *Delegator) Info(ctx context.Context) (ports.DelegatorInfo, error) {
	var info struct {
		PubKey string `json:"pubkey"`
	}
	if err := d.request(ctx, "GET", "/v1/delegator/info", nil, &info); err != nil {
		return ports.DelegatorInfo{}, err
	}
	if len(info.PubKey) != 66 {
		return ports.DelegatorInfo{}, errors.New("invalid delegator public key")
	}
	return ports.DelegatorInfo{PubKey: info.PubKey}, nil
}

var _ ports.Delegator = (*Delegator)(nil)
