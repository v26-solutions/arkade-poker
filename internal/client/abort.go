package client

import (
	"context"
	"errors"

	"arkade-poker/go/internal/game"
	"arkade-poker/go/internal/storage"
)

var ErrSetupFunded = errors.New("client: covenant funding has begun; saved game retained, restart to resume")

// No services or replay effects run here. Spending is journaled before signing
// or submission, so only a current journal containing setup facts may be erased.
// Earlier completed hands do not prevent abandoning the current setup.
func (c *Client) checkUnfundedSetup(ctx context.Context, public [32]byte) error {
	if c.config.Open == nil {
		return storage.ErrUnavailable
	}
	log, err := c.config.Open(ctx, public)
	if err != nil {
		return err
	}
	defer log.Close()
	records, err := log.Load(ctx)
	if err != nil {
		return err
	}
	defer clearRecords(records)
	for i := len(records) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		e, err := game.DecodeEvent(records[i])
		if err != nil {
			return err
		}
		if e.Secrets != nil {
			_ = e.Secrets.Destroy()
		}
		switch e.Kind {
		case game.Configured:
			return e.CheckWallet(public)
		case game.SessionPrepared, game.SessionOpened, game.MessagePrepared,
			game.PublicationPrepared, game.MessagePublished, game.MessageReceived, game.SetupAborted:
		default:
			return ErrSetupFunded
		}
	}
	if len(records) != 0 {
		return game.ErrOrder
	}
	return nil // Cancellation can precede the first saved setup fact.
}
