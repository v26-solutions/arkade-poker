package wallet

import (
	"context"
	"errors"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

var ErrOwnership = errors.New("wallet: invalid ownership proof")

// ProveOwnership delegates BIP-340 to btcec. The imported key is never exported.
func (k *Key) ProveOwnership(ctx context.Context, digest [32]byte) ([]byte, error) {
	if ctx == nil {
		return nil, ErrOwnership
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if k == nil || k.secret == nil || k.secret.Key.IsZero() {
		return nil, ErrKey
	}
	signature, err := schnorr.Sign(k.secret, digest[:])
	if err != nil {
		return nil, ErrOwnership
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return signature.Serialize(), nil
}
func AuthenticateOwnership(ctx context.Context, key, digest [32]byte, proof []byte) error {
	if ctx == nil {
		return ErrOwnership
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	public, err := schnorr.ParsePubKey(key[:])
	if err != nil {
		return ErrOwnership
	}
	signature, err := schnorr.ParseSignature(proof)
	if err != nil || !signature.Verify(digest[:], public) {
		return ErrOwnership
	}
	return nil
}
func (w *Wallet) ProveOwnership(ctx context.Context, digest [32]byte) ([]byte, error) {
	if w == nil {
		return nil, ErrKey
	}
	return w.key.ProveOwnership(ctx, digest)
}
func (w *Wallet) AuthenticateOwnership(ctx context.Context, key, digest [32]byte, proof []byte) error {
	return AuthenticateOwnership(ctx, key, digest, proof)
}
