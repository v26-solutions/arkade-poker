package indexdata

import (
	"bytes"
	"encoding/hex"
	"errors"
	"slices"

	"arkade-poker/go/internal/ports"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

const MaxWatchScripts = 16
const MaxEventVtxos = 256

// WatchScripts owns the filter before any asynchronous work starts.
func WatchScripts(scripts [][]byte) ([]string, error) {
	if len(scripts) == 0 || len(scripts) > MaxWatchScripts {
		return nil, errors.New("subscription requires 1–16 scripts")
	}
	encoded := make([]string, len(scripts))
	for i, script := range scripts {
		if !txscript.IsPayToTaproot(script) {
			return nil, errors.New("subscription requires P2TR scripts")
		}
		encoded[i] = hex.EncodeToString(script)
		if slices.Contains(encoded[:i], encoded[i]) {
			return nil, errors.New("duplicate subscription script")
		}
	}
	return encoded, nil
}

type EventVtxo struct {
	TxID, Script string
	Vout         uint32
}

// SubscriptionFrame contains only observation hints. Transaction bodies,
// signatures, amounts and acceptance are obtained through the query boundary.
// Exactly one of Started, Heartbeat or Event must be present.
type SubscriptionFrame struct {
	Started   string
	Heartbeat bool
	Event     *TransactionEvent
}

type TransactionEvent struct {
	TxID       string
	New, Spent []EventVtxo
}

func (f SubscriptionFrame) Events(scripts []string) ([]ports.ScriptEvent, error) {
	kinds := 0
	if f.Started != "" {
		kinds++
	}
	if f.Heartbeat {
		kinds++
	}
	if f.Event != nil {
		kinds++
	}
	if kinds != 1 {
		return nil, errors.New("invalid subscription frame kind")
	}
	if f.Started != "" {
		if len(f.Started) > 128 {
			return nil, errors.New("invalid subscription id")
		}
		for _, c := range f.Started {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return nil, errors.New("invalid subscription id")
			}
		}
	}
	if f.Event == nil {
		return nil, nil
	}
	id, err := Hash(f.Event.TxID)
	if err != nil {
		return nil, err
	}
	points := func(records []EventVtxo) (map[string][]wire.OutPoint, error) {
		if len(records) > MaxEventVtxos {
			return nil, errors.New("subscription outpoint limit")
		}
		result := make(map[string][]wire.OutPoint)
		for _, record := range records {
			hash, err := Hash(record.TxID)
			if err != nil {
				return nil, err
			}
			if len(record.Script) == 0 || len(record.Script) > 2*txscript.MaxScriptSize {
				return nil, errors.New("invalid subscription output script")
			}
			script, err := hex.DecodeString(record.Script)
			if err != nil {
				return nil, errors.New("invalid subscription output script")
			}
			key := hex.EncodeToString(script)
			if !slices.Contains(scripts, key) {
				continue
			}
			point := wire.OutPoint{Hash: hash, Index: record.Vout}
			if !slices.Contains(result[key], point) {
				result[key] = append(result[key], point)
			}
		}
		for key := range result {
			slices.SortFunc(result[key], func(a, b wire.OutPoint) int {
				if n := bytes.Compare(a.Hash[:], b.Hash[:]); n != 0 {
					return n
				}
				if a.Index < b.Index {
					return -1
				}
				if a.Index > b.Index {
					return 1
				}
				return 0
			})
		}
		return result, nil
	}
	created, err := points(f.Event.New)
	if err != nil {
		return nil, err
	}
	spent, err := points(f.Event.Spent)
	if err != nil {
		return nil, err
	}
	events := make([]ports.ScriptEvent, len(scripts))
	for i, key := range scripts {
		script, _ := hex.DecodeString(key)
		events[i] = ports.ScriptEvent{Kind: ports.ScriptChanged, Script: script, TxID: id, NewVtxos: created[key], SpentVtxos: spent[key]}
	}
	return events, nil
}
