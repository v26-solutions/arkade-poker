// Package client owns one local wallet's host lifecycle. The game driver alone
// runs poker transitions; this package connects ports and retains completed logs.
package client

import (
	"context"

	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/storage"
)

// A wallet log is a concatenation of canonical game logs. A new start marker
// may follow only a replay-validated terminal outcome. Appending a new game
// preserves earlier records; explicit ClearSavedGame removes the entire log.
// The physical wallet writer lock stays held across game boundaries.
type sessionLog struct {
	base   storage.Log
	offset uint64
}

func (s *sessionLog) Load(ctx context.Context) ([][]byte, error) {
	r, err := s.base.Load(ctx)
	if err != nil {
		return nil, err
	}
	if s.offset > uint64(len(r)) {
		clearRecords(r)
		return nil, storage.ErrIndex
	}
	clearRecords(r[:s.offset])
	return r[s.offset:], nil
}
func (s *sessionLog) Append(ctx context.Context, i uint64, b []byte) error {
	if i+s.offset < i {
		return storage.ErrIndex
	}
	return s.base.Append(ctx, i+s.offset, b)
}
func (*sessionLog) Close() error { return nil } // Client owns the physical store.
func clearRecords(records [][]byte) {
	for _, r := range records {
		clear(r)
	}
}

func currentSession(ctx context.Context, base storage.Log, config game.Config) (*sessionLog, error) {
	r, err := base.Load(ctx)
	if err != nil {
		return nil, err
	}
	defer clearRecords(r)
	var start int
	for i, raw := range r {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e, err := game.DecodeEvent(raw)
		if err != nil {
			return nil, err
		}
		if e.Secrets != nil {
			_ = e.Secrets.Destroy()
		}
		if e.Kind != game.Configured {
			if i == 0 {
				return nil, game.ErrOrder
			}
			continue
		}
		if err := e.CheckWallet(config.Wallet.WalletPublicKey); err != nil {
			return nil, err
		}
		if i != 0 {
			if err := completed(ctx, config, r[start:i]); err != nil {
				return nil, err
			}
		}
		start = i
	}
	return &sessionLog{base: base, offset: uint64(start)}, nil
}

func completed(ctx context.Context, config game.Config, records [][]byte) error {
	var g *game.Game
	defer func() {
		if g != nil {
			g.Destroy()
		}
	}()
	for _, raw := range records {
		e, err := game.DecodeEvent(raw)
		if err != nil {
			return err
		}
		err = func() error {
			if e.Secrets != nil {
				defer e.Secrets.Destroy()
			}
			if g == nil {
				if e.Kind != game.Configured {
					return game.ErrOrder
				}
				g, err = game.New(config)
				if err != nil {
					return err
				}
			}
			return g.ApplyContext(ctx, e)
		}()
		if err != nil {
			return err
		}
	}
	if g == nil {
		return game.ErrOrder
	}
	s, err := g.Snapshot()
	if err != nil {
		return err
	}
	if s.Outcome == nil || s.Stage != game.StageFinished && s.Stage != game.StageAborted {
		return game.ErrOrder
	}
	return nil
}
