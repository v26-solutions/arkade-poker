module arkade-poker/go

go 1.26.6

require (
	charm.land/bubbles/v2 v2.0.0
	charm.land/bubbletea/v2 v2.0.6
	charm.land/lipgloss/v2 v2.0.3
	github.com/ArkLabsHQ/enclave v0.0.80-0.20260918130722-dc4b91624d2d
	github.com/NimbleMarkets/go-booba v0.6.1-0.20260502031901-87edfeeafa5e
	github.com/arkade-os/arkd/api-spec v0.0.0-20260829095256-13a3313857fb
	github.com/arkade-os/arkd/pkg/ark-lib v0.8.1-0.20260829095256-13a3313857fb
	github.com/arkade-os/arkd/pkg/client-lib v0.0.0-20260829095256-13a3313857fb
	github.com/arkade-os/emulator/pkg/client v0.0.0-20260903164234-4feb9eaa81b4
	github.com/atotto/clipboard v0.1.4
	github.com/btcsuite/btcd v0.24.3-0.20240921052913-67b8efd3ba53
	github.com/btcsuite/btcd/btcec/v2 v2.3.5
	github.com/btcsuite/btcd/btcutil v1.1.6
	github.com/btcsuite/btcd/btcutil/psbt v1.1.9
	github.com/btcsuite/btcd/chaincfg/chainhash v1.1.0
	github.com/btcsuite/btcwallet v0.16.10-0.20240718224643-db3a4a2543bd
	github.com/coder/websocket v1.8.14
	github.com/evanw/esbuild v0.28.2
	github.com/meshapi/grpc-api-gateway v0.1.0
	github.com/sirupsen/logrus v1.9.3
	google.golang.org/grpc v1.82.1
)

require (
	github.com/bits-and-blooms/bitset v1.24.4 // indirect
	github.com/btcsuite/btcwallet/walletdb v1.4.2 // indirect
	github.com/consensys/gnark-crypto v0.19.2 // indirect
	github.com/fxamacker/cbor/v2 v2.9.2 // indirect
	github.com/hf/nitrite v0.0.0-20241225144000-c2d5d3c4f303 // indirect
	github.com/lightninglabs/neutrino/cache v1.1.2 // indirect
	github.com/lightningnetwork/lnd/fn v1.2.1 // indirect
	github.com/lightningnetwork/lnd/tlv v1.2.6 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/x448/float16 v0.8.4 // indirect
)

require (
	cel.dev/expr v0.25.1 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.0 // indirect
	github.com/arkade-os/arkd/pkg/errors v0.0.0-20260829095256-13a3313857fb // indirect
	github.com/arkade-os/emulator/api-spec v0.0.0-20260903164234-4feb9eaa81b4
	github.com/arkade-os/emulator/pkg/arkade v0.0.0-20260903164234-4feb9eaa81b4
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260422141423-a0f1f21775f7 // indirect
	github.com/charmbracelet/x/ansi v0.11.7
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/decred/dcrd/crypto/blake256 v1.1.0 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.0 // indirect
	github.com/google/cel-go v0.26.1 // indirect
	github.com/julienschmidt/httprouter v1.3.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.0 // indirect
	github.com/mattn/go-runewidth v0.0.23 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/stoewer/go-strcase v1.2.0 // indirect
	github.com/tyler-smith/go-bip39 v1.1.0
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	golang.org/x/crypto v0.53.0
	golang.org/x/exp v0.0.0-20250106191152-7588d65b2ba8 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	google.golang.org/genproto v0.0.0-20240812133136-8ffd90a71988 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

// Keep the nested Arkd/emulator modules on compatible revisions throughout the
// application graph; dependency replace directives are not inherited.
replace (
	github.com/arkade-os/arkd/api-spec => github.com/arkade-os/arkd/api-spec v0.0.0-20260829095256-13a3313857fb
	github.com/arkade-os/arkd/pkg/ark-lib => github.com/arkade-os/arkd/pkg/ark-lib v0.8.1-0.20260829095256-13a3313857fb
	github.com/arkade-os/arkd/pkg/client-lib => github.com/arkade-os/arkd/pkg/client-lib v0.0.0-20260829095256-13a3313857fb
	github.com/arkade-os/arkd/pkg/errors => github.com/arkade-os/arkd/pkg/errors v0.0.0-20260829095256-13a3313857fb
	github.com/arkade-os/emulator/api-spec => github.com/arkade-os/emulator/api-spec v0.0.0-20260903164234-4feb9eaa81b4
	github.com/arkade-os/emulator/pkg/arkade => github.com/arkade-os/emulator/pkg/arkade v0.0.0-20260903164234-4feb9eaa81b4
	github.com/arkade-os/emulator/pkg/client => github.com/arkade-os/emulator/pkg/client v0.0.0-20260903164234-4feb9eaa81b4
)
