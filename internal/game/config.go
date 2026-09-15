package game

import (
	"bytes"
	"unicode/utf8"

	"github.com/btcsuite/btcd/txscript"
)

func (c Config) validate() error {
	for _, k := range [][32]byte{c.Wallet.WalletPublicKey, c.Wallet.ArkSigningKey, c.Wallet.EmulatorSigningKey} {
		if !validXOnly(k) {
			return ErrEncoding
		}
	}
	if len(c.Wallet.CheckpointScript) == 0 || len(c.Wallet.CheckpointScript) > maxScriptBytes {
		return ErrEncoding
	}
	p := c.Wallet.OutputPolicy
	if p.MinAmount <= 0 || p.MinAmount > p.MaxAmount || p.MaxAmount > maxMoney {
		return ErrAmount
	}
	if len(c.Wallet.Receive.Script) > maxScriptBytes || len(c.Wallet.Receive.Tapscripts) > 256 {
		return ErrEncoding
	}
	for _, s := range append([]string{c.ArkdURL, c.EmulatorURL, c.DelegatorURL, c.IndexerURL, c.Wallet.Receive.Address}, c.Wallet.Receive.Tapscripts...) {
		if !utf8.ValidString(s) || len(s) > 2*maxScriptBytes {
			return ErrEncoding
		}
	}
	return nil
}

func serviceFields(w *encoder, c Config) {
	w.str(c.Wallet.Network, 16)
	w.raw(c.Wallet.ArkSigningKey[:])
	w.raw(c.Wallet.EmulatorSigningKey[:])
	w.blob(c.Wallet.CheckpointScript, maxScriptBytes)
	w.u64(uint64(c.Wallet.OutputPolicy.MinAmount))
	w.u64(uint64(c.Wallet.OutputPolicy.MaxAmount))
}
func serviceBinding(c Config) ([32]byte, error) {
	if err := c.validate(); err != nil {
		return [32]byte{}, err
	}
	w := new(encoder)
	w.header("arkade-poker/services\x00")
	serviceFields(w, c)
	b, err := w.finish()
	if err != nil {
		return [32]byte{}, err
	}
	return digest("", b), nil
}

// Config encoding is used for owned runtime copies and legacy event decoding.
// New Configured events contain no configuration payload.
func encodeConfig(c Config) ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	w := new(encoder)
	w.header(configDomain)
	serviceFields(w, c)
	w.raw(c.Wallet.WalletPublicKey[:])
	w.str(c.Wallet.Receive.Address, 2*maxScriptBytes)
	w.blob(c.Wallet.Receive.Script, maxScriptBytes)
	w.u32(uint32(len(c.Wallet.Receive.Tapscripts)))
	for _, s := range c.Wallet.Receive.Tapscripts {
		w.str(s, 2*maxScriptBytes)
	}
	for _, s := range []string{c.ArkdURL, c.EmulatorURL, c.IndexerURL} {
		w.str(s, 2*maxScriptBytes)
	}
	// Optional tail preserves the canonical bytes of pre-delegator sessions.
	// The delegate key is already committed by Receive.Script and Tapscripts.
	if c.DelegatorURL != "" {
		w.str(c.DelegatorURL, 2*maxScriptBytes)
	}
	return w.finish()
}
func decodeConfig(data []byte) (Config, error) {
	if len(data) > MaxEventBytes {
		return Config{}, ErrEncoding
	}
	r := &decoder{data: data}
	r.header(configDomain)
	var c Config
	c.Wallet.Network = r.str(16)
	copy(c.Wallet.ArkSigningKey[:], r.raw(32))
	copy(c.Wallet.EmulatorSigningKey[:], r.raw(32))
	c.Wallet.CheckpointScript = r.blob(maxScriptBytes)
	c.Wallet.OutputPolicy.MinAmount, c.Wallet.OutputPolicy.MaxAmount = int64(r.u64()), int64(r.u64())
	copy(c.Wallet.WalletPublicKey[:], r.raw(32))
	c.Wallet.Receive.Address = r.str(2 * maxScriptBytes)
	c.Wallet.Receive.Script = r.blob(maxScriptBytes)
	n := r.u32()
	if n > 256 || uint64(n)*4 > uint64(len(data)-r.pos) {
		return Config{}, ErrEncoding
	}
	for range n {
		c.Wallet.Receive.Tapscripts = append(c.Wallet.Receive.Tapscripts, r.str(2*maxScriptBytes))
	}
	c.ArkdURL, c.EmulatorURL, c.IndexerURL = r.str(2*maxScriptBytes), r.str(2*maxScriptBytes), r.str(2*maxScriptBytes)
	if r.pos < len(data) {
		c.DelegatorURL = r.str(2 * maxScriptBytes)
	}
	if err := r.done(); err != nil {
		return Config{}, err
	}
	b, err := encodeConfig(c)
	if err != nil {
		return Config{}, err
	}
	if !bytes.Equal(b, data) {
		return Config{}, ErrEncoding
	}
	return c, nil
}
func policyOutput(c Config, script []byte, amount int64) error {
	if !txscript.IsPayToTaproot(script) || amount < c.Wallet.OutputPolicy.MinAmount || amount > c.Wallet.OutputPolicy.MaxAmount {
		return ErrAmount
	}
	return nil
}
