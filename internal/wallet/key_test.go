package wallet

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	arklib "github.com/arkade-os/arkd/pkg/ark-lib"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil/bech32"
)

func TestKeyIngress(t *testing.T) {
	// Public test vector: private scalar 1, secp256k1 generator x coordinate.
	hexKey := strings.Repeat("0", 63) + "1"
	data, _ := hex.DecodeString(hexKey)
	words, _ := bech32.ConvertBits(data, 8, 5, true)
	nsec, _ := bech32.Encode("nsec", words)
	for _, input := range []string{
		hexKey, nsec, strings.ToUpper(nsec),
		" " + hexKey, hexKey + "\n", "\t\r\n" + hexKey + "\u00a0 ",
		" " + nsec, nsec + "\n", "\t\r\n" + nsec + "\u00a0 ",
	} {
		k, err := ParseKey(input, "")
		if err != nil {
			t.Fatal(err)
		}
		pub := k.PublicKey()
		if hex.EncodeToString(pub[:]) != "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798" {
			t.Fatal("wrong public key")
		}
		if _, err := json.Marshal(k); err == nil {
			t.Fatal("private key serialized")
		}
		if strings.Contains(fmt.Sprintf("%v %#v", k, k), hexKey) {
			t.Fatal("private key logged")
		}
		k.Destroy()
	}
	wrongPrefix, _ := bech32.Encode("npub", words)
	wrongChecksumType, _ := bech32.EncodeM("nsec", words)
	short, _ := bech32.Encode("nsec", words[:len(words)-1])
	invalid := []string{"", " \t\r\n\u00a0", hexKey + "0", "0x" + hexKey,
		hexKey[:32] + " " + hexKey[32:], nsec[:31] + " " + nsec[31:],
		strings.Repeat("0", 64), strings.Repeat("f", 64),
		"fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141", // N
		wrongPrefix, wrongChecksumType, short, nsec[:62] + "!", "nSeC" + nsec[4:], "abandon abandon abandon"}
	for i, input := range invalid {
		k, err := ParseKey(input, "")
		if err != ErrKey || k != nil {
			t.Errorf("invalid case %d accepted", i)
		}
	}
}

const testMnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

func TestMnemonicIngress(t *testing.T) {
	// Mainnet is the published BIP86 first receiving internal-key vector:
	// https://github.com/bitcoin/bips/blob/master/bip-0086.mediawiki#test-vectors
	// Testnet and longer phrases were independently derived with Python
	// PBKDF2/HMAC and secp256k1 arithmetic (empty passphrase, account/index 0).
	const mainnetPub = "cc8a4bc64d897bddc5fbc2f670f7a8ba0b386779106cf1223c6fc5d7cd6fc115"
	const testnetPub = "55355ca83c973f1d97ce0e3843c85d78905af16b4dc531bc488e57212d230116"
	for _, network := range []string{"bitcoin", "testnet", "testnet4", "signet", "mutinynet", "regtest"} {
		t.Run(network, func(t *testing.T) {
			want := testnetPub
			if network == "bitcoin" {
				want = mainnetPub
			}
			for _, text := range []string{testMnemonic, " \t" + strings.ReplaceAll(testMnemonic, " ", "  \n") + "\n"} {
				key, err := ParseKey(text, network)
				if err != nil {
					t.Fatal(err)
				}
				pub := key.PublicKey()
				if hex.EncodeToString(pub[:]) != want {
					t.Fatal("wrong mnemonic derivation")
				}
				if _, err := json.Marshal(key); err == nil {
					t.Fatal("mnemonic key serialized")
				}
				if strings.Contains(fmt.Sprintf("%v %#v", key, key), "abandon") {
					t.Fatal("mnemonic logged")
				}
				key.Destroy()
				if !key.secret.Key.IsZero() {
					t.Fatal("mnemonic key not destroyed")
				}
			}
		})
	}
	for _, tc := range []struct {
		words        int
		last, public string
	}{
		{15, "address", "6365d120646cb2d9c890c56b984cf9955d5cb9efa15d2388cda2d56f67d8789d"},
		{18, "agent", "5e8c484c982746acf1a22b73505a2fd12eaed63aa85135099b06fa744e7ffbef"},
		{21, "admit", "25024b1545a2e99e0feffc2a7d935faf92598149ff8795ccfc32df03e99ac22f"},
		{24, "art", "001291984ed14a7efb6a84ce236f654e8d4c4b8794162d6ec9de44f8e4119dd5"},
	} {
		t.Run(fmt.Sprintf("%d words", tc.words), func(t *testing.T) {
			key, err := ParseKey(strings.Repeat("abandon ", tc.words-1)+tc.last, "bitcoin")
			if err != nil {
				t.Fatal(err)
			}
			defer key.Destroy()
			pub := key.PublicKey()
			if hex.EncodeToString(pub[:]) != tc.public {
				t.Fatal("wrong mnemonic derivation")
			}
		})
	}
}

func TestMnemonicRejection(t *testing.T) {
	for _, network := range []string{"", "unknown", "unavailable", "mainnet"} {
		if key, err := ParseKey(testMnemonic, network); key != nil || err != ErrKeyNetwork {
			t.Fatalf("network %q did not reject mnemonic: %v", network, err)
		}
	}
	for _, text := range []string{
		strings.Repeat("abandon ", 12), // Valid words, invalid checksum.
		strings.Replace(testMnemonic, "about", "secret-not-in-wordlist", 1),
		testMnemonic + " about", // Invalid word count.
		strings.ToUpper(testMnemonic),
	} {
		for _, network := range []string{"", "bitcoin"} {
			if key, err := ParseKey(text, network); key != nil || err != ErrKey {
				t.Fatal("invalid mnemonic accepted or input exposed in error")
			}
		}
	}
}

func TestMnemonicReceiveNetwork(t *testing.T) {
	key, err := ParseKey(testMnemonic, "regtest")
	if err != nil {
		t.Fatal(err)
	}
	defer key.Destroy()
	server, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{2}, 32))
	defer server.Zero()
	delay := arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 144}
	if _, err := key.Receive(server.PubKey(), nil, delay, "bitcoin"); err != ErrKeyNetwork {
		t.Fatal("mnemonic key accepted a different network after discovery")
	}
	if _, err := key.Receive(server.PubKey(), nil, delay, "regtest"); err != nil {
		t.Fatal(err)
	}
}

func TestReceiveAddress(t *testing.T) {
	k, _ := ParseKey(strings.Repeat("0", 63)+"1", "")
	defer k.Destroy()
	server, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{2}, 32))
	r, err := k.Receive(server.PubKey(), nil, arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 144}, "regtest")
	if err != nil {
		t.Fatal(err)
	}
	a, err := arklib.DecodeAddressV0(r.Address)
	if err != nil {
		t.Fatal(err)
	}
	script, _ := a.GetPkScript()
	if !bytes.Equal(script, r.Script) || len(r.Tapscripts) != 2 {
		t.Fatal("address does not describe payout tree")
	}
	for _, delay := range []arklib.RelativeLocktime{
		{Type: arklib.LocktimeTypeBlock, Value: 65536}, {Type: arklib.LocktimeTypeBlock, Value: 0},
		{Type: arklib.LocktimeTypeSecond, Value: 513}, {Type: 99, Value: 144},
	} {
		if _, err := k.Receive(server.PubKey(), nil, delay, "regtest"); err == nil {
			t.Fatal("invalid delay accepted")
		}
	}
}

func TestReceiveNetworks(t *testing.T) {
	k, _ := ParseKey(strings.Repeat("0", 63)+"1", "")
	defer k.Destroy()
	server, _ := btcec.PrivKeyFromBytes(bytes.Repeat([]byte{2}, 32))
	defer server.Zero()
	for network, prefix := range map[string]string{"mutinynet": "tark1", "regtest": "tark1", "bitcoin": "ark1", "unknown": "ark1"} {
		r, err := k.Receive(server.PubKey(), nil, arklib.RelativeLocktime{Type: arklib.LocktimeTypeBlock, Value: 144}, network)
		if err != nil || !strings.HasPrefix(r.Address, prefix) {
			t.Fatalf("%s: %s, %v", network, r.Address, err)
		}
		a, err := arklib.DecodeAddressV0(r.Address)
		if err != nil {
			t.Fatal(err)
		}
		output, err := a.GetPkScript()
		if err != nil || !bytes.Equal(output, r.Script) {
			t.Fatalf("%s: payout script mismatch: %v", network, err)
		}
	}
}
