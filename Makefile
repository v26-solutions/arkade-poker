GO ?= go
export GO
BUILD = $(GO) run ./scripts/build
WEB_FLAGS ?=

.PHONY: all native web test test-wasm test-regtest-hands check clean serve source-release release qualify-shuffle qualify-storage qualify-transport
all: native web

native:
	mkdir -p build
	CGO_ENABLED=0 $(GO) build -tags=purego -trimpath -buildvcs=false -o build/poker ./cmd/poker

web:
	$(BUILD) web $(WEB_FLAGS)

test:
	CGO_ENABLED=0 $(GO) test -tags=purego ./...

test-wasm:
	CGO_ENABLED=0 GOOS=js GOARCH=wasm $(GO) test -exec='$(CURDIR)/web/wasm-test.sh' ./internal/appconfig ./internal/covenant ./internal/shuffle ./internal/game ./internal/client ./internal/storage ./internal/merkel ./internal/wallet ./internal/nostr ./internal/adapters/indexdata ./internal/adapters/subscription ./internal/adapters/http

check: test all
	$(GO) vet ./...

# Explicit opt-in: funds fresh test wallets from the existing local regtest faucet.
test-regtest-hands:
	CGO_ENABLED=0 $(GO) test -tags=purego,regtest_hand ./internal/integration -v -count=1 -timeout=20m

source-release:
	$(BUILD) source

release: all source-release
	$(BUILD) artifacts

serve: web
	$(BUILD) serve web

qualify-shuffle:
	$(BUILD) qualify shuffle
	$(BUILD) serve shuffle

qualify-storage:
	$(BUILD) qualify storage
	$(BUILD) serve storage

qualify-transport:
	$(BUILD) qualify transport
	$(GO) run ./cmd/transport-fixture

clean:
	rm -rf build
