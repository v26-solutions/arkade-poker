package game

import (
	"context"
	"errors"

	"arkade-poker/go/internal/storage"
)

var ErrWalletIdentity = errors.New("game: supplied wallet does not own the saved session")

// journal is owned by the sequential driver, never concurrently by UI commands.
// The candidate is admitted before writing so invalid evidence cannot poison a
// valid log, but it becomes the live game only after durable acknowledgement.
// Any failed append permanently stops this writer until Close and fresh replay.
type journal struct {
	game   *Game
	log    storage.Log
	failed bool
}

func loadJournal(ctx context.Context, config Config, log storage.Log) (_ *journal, err error) {
	if ctx == nil || log == nil {
		return nil, ErrInput
	}
	records, err := log.Load(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, record := range records {
			clear(record)
		}
	}()
	g, err := New(config)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			discardGame(g, nil)
		}
	}()
	for i, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		event, err := DecodeEvent(record)
		if err != nil {
			return nil, err
		}
		err = func() error {
			if event.Secrets != nil {
				defer event.Secrets.Destroy()
			}
			if i == 0 {
				if err := event.CheckWallet(config.Wallet.WalletPublicKey); err != nil {
					return err
				}
			}
			return g.ApplyContext(ctx, event)
		}()
		if err != nil {
			return nil, err
		}
	}
	return &journal{game: g, log: log}, nil
}

func (j *journal) append(ctx context.Context, event Event) error {
	if j.failed {
		return storage.ErrUncertain
	}
	if ctx == nil {
		return ErrInput
	}
	record, err := EncodeEvent(event)
	if err != nil {
		return err
	}
	defer clear(record)
	owned, err := DecodeEvent(record)
	if err != nil {
		return err
	}
	if owned.Secrets != nil {
		defer owned.Secrets.Destroy()
	}
	candidate := *j.game
	if err := candidate.ApplyContext(ctx, owned); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			discardGame(&candidate, j.game)
		}
	}()
	if err := j.log.Append(ctx, j.game.nextEvent, record); err != nil {
		j.failed = true
		return err
	}
	// Do not recheck cancellation here: a successful Append means this fact is
	// durable. Publishing the already admitted candidate cannot fail afterwards.
	j.game = &candidate
	committed = true
	return nil
}

func discardGame(g, previous *Game) {
	if g != nil && g.setup != nil && (previous == nil || previous.setup == nil || g.setup.secrets != previous.setup.secrets) {
		_ = g.setup.secrets.Destroy()
	}
}
