package wallet

import (
	"bytes"
	"encoding/base64"
	"errors"

	"arkade-poker/go/internal/ports"
	"github.com/arkade-os/arkd/pkg/ark-lib/txutils"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

// packetMaps delegates non-signature admission to the same upstream PSBT
// parser used by the driver, while retaining exact opaque maps for merging.
func packetMaps(encoded string) (*psbt.Packet, [][]psbtField, error) {
	p, _, err := unsignedPSBT(encoded)
	if err != nil {
		return nil, nil, err
	}
	data, _ := base64.StdEncoding.DecodeString(encoded)
	r := bytes.NewReader(data[5:])
	maps := make([][]psbtField, 1+len(p.Inputs)+len(p.Outputs))
	for i := range maps {
		maps[i], err = readPSBTMap(r)
		if err != nil {
			return nil, nil, err
		}
	}
	return p, maps, nil
}

func encodeMaps(maps [][]psbtField) string {
	var b bytes.Buffer
	b.Write([]byte{'p', 's', 'b', 't', 0xff})
	for _, fields := range maps {
		writePSBTMap(&b, fields, false)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func equalFields(a, b []psbtField, skipSignatures bool) bool {
	var left, right bytes.Buffer
	writePSBTMap(&left, a, skipSignatures)
	writePSBTMap(&right, b, skipSignatures)
	return bytes.Equal(left.Bytes(), right.Bytes())
}

func mergePSBT(base, remote string, rebuilt bool) (string, error) {
	p, local, err := packetMaps(base)
	if err != nil {
		return "", err
	}
	q, actual, err := packetMaps(remote)
	if err != nil {
		return "", err
	}
	if p.UnsignedTx.TxHash() != q.UnsignedTx.TxHash() || len(local) != len(actual) || !equalFields(local[0], actual[0], false) {
		return "", errors.New("wallet: service changed transaction or global metadata")
	}
	for i := range p.Outputs {
		index := 1 + len(p.Inputs) + i
		if !equalFields(local[index], actual[index], false) {
			return "", errors.New("wallet: service changed output metadata")
		}
	}
	// Arkd's BuildTxs checkpoints retain only these upstream input fields.
	// Permit precisely that projection, then merge into the saved full maps.
	treeKey := append([]byte{txutils.ArkPsbtFieldKeyType}, txutils.ArkFieldTaprootTree...)
	for i := range p.Inputs {
		left, right := local[i+1], actual[i+1]
		if !equalFields(left, right, true) {
			if !rebuilt {
				return "", errors.New("wallet: service changed input metadata")
			}
			var expected []psbtField
			foundTree := false
			for _, f := range left {
				isTree := bytes.Equal(f.key, treeKey)
				if isTree {
					foundTree = true
				}
				if isTree || bytes.Equal(f.key, []byte{byte(psbt.WitnessUtxoType)}) || f.key[0] == byte(psbt.TaprootLeafScriptType) {
					expected = append(expected, f)
				}
			}
			if !foundTree || !equalFields(expected, right, true) {
				return "", errors.New("wallet: service changed rebuilt checkpoint")
			}
		}
		remoteSigs := make(map[string][]byte)
		for _, f := range right {
			if f.key[0] == byte(psbt.TaprootScriptSpendSignatureType) {
				remoteSigs[string(f.key)] = f.value
			}
		}
		for _, f := range left {
			if f.key[0] != byte(psbt.TaprootScriptSpendSignatureType) {
				continue
			}
			v, ok := remoteSigs[string(f.key)]
			if (ok && !bytes.Equal(v, f.value)) || (!rebuilt && !ok) {
				return "", errors.New("wallet: service changed local signature")
			}
			delete(remoteSigs, string(f.key))
		}
		for k, v := range remoteSigs {
			left = append(left, psbtField{[]byte(k), v})
		}
		local[i+1] = left
	}
	return encodeMaps(local), nil
}

func mergeBundles(base, remote ports.Bundle, rebuiltCheckpoints bool) (ports.Bundle, error) {
	if err := bundleBound(base); err != nil {
		return ports.Bundle{}, err
	}
	if err := bundleBound(remote); err != nil {
		return ports.Bundle{}, err
	}
	if len(base.Checkpoints) != len(remote.Checkpoints) || len(base.Checkpoints) > 256 {
		return ports.Bundle{}, errors.New("wallet: service checkpoint count")
	}
	main, err := mergePSBT(base.Ark, remote.Ark, false)
	if err != nil {
		return ports.Bundle{}, err
	}
	indexed := make(map[chainhash.Hash]string)
	for _, encoded := range remote.Checkpoints {
		cp, _, err := unsignedPSBT(encoded)
		if err != nil {
			return ports.Bundle{}, err
		}
		id := cp.UnsignedTx.TxHash()
		if _, ok := indexed[id]; ok {
			return ports.Bundle{}, errors.New("wallet: duplicate service checkpoint")
		}
		indexed[id] = encoded
	}
	result := ports.Bundle{Ark: main}
	for _, encoded := range base.Checkpoints {
		cp, _, err := unsignedPSBT(encoded)
		if err != nil {
			return ports.Bundle{}, err
		}
		id := cp.UnsignedTx.TxHash()
		matching, ok := indexed[id]
		if !ok {
			return ports.Bundle{}, errors.New("wallet: missing service checkpoint")
		}
		delete(indexed, id)
		merged, err := mergePSBT(encoded, matching, rebuiltCheckpoints)
		if err != nil {
			return ports.Bundle{}, err
		}
		result.Checkpoints = append(result.Checkpoints, merged)
	}
	if err := CompareBundles(base, result); err != nil {
		return ports.Bundle{}, err
	}
	return result, nil
}
