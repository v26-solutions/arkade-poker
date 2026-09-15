#!/bin/sh
set -eu

rpc=http://anvil:8545
swap=0x00000000000000000000000000000000deadbeef
token=0xc02aaa39b223fe8d0a0e5c4f27ead9083c756cc2
max=0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
zero=$(cast to-uint256 0)
expected_send_balance=$(cast to-uint256 "$EVM_LIQUIDITY_WEI")
expected_client_balance=$(cast to-uint256 "$EVM_CLIENT_LIQUIDITY_WEI")

until cast chain-id --rpc-url "$rpc" >/dev/null 2>&1; do sleep 1; done

swap_runtime=$(tr -d '\r\n' </fixtures/erc20swap.runtime.hex)
token_runtime=$(tr -d '\r\n' </fixtures/weth9.runtime.hex)
test "$(cast keccak "$swap_runtime")" = "$EVM_SWAP_CODE_HASH"
test "$(cast keccak "$token_runtime")" = "$EVM_TOKEN_CODE_HASH"

cast rpc --rpc-url "$rpc" anvil_setCode "$swap" "$swap_runtime" >/dev/null
cast rpc --rpc-url "$rpc" anvil_setCode "$token" "$token_runtime" >/dev/null
test "$(cast keccak "$(cast code --rpc-url "$rpc" "$swap")")" = "$EVM_SWAP_CODE_HASH"
test "$(cast keccak "$(cast code --rpc-url "$rpc" "$token")")" = "$EVM_TOKEN_CODE_HASH"

read_state() {
  send_balance=$(cast call --rpc-url "$rpc" --data "$(cast calldata 'balanceOf(address)' "$EVM_SEND_ADDRESS")" "$token")
  send_allowance=$(cast call --rpc-url "$rpc" --data "$(cast calldata 'allowance(address,address)' "$EVM_SEND_ADDRESS" "$swap")" "$token")
  client_balance=$(cast call --rpc-url "$rpc" --data "$(cast calldata 'balanceOf(address)' "$EVM_CLIENT_ADDRESS")" "$token")
  client_allowance=$(cast call --rpc-url "$rpc" --data "$(cast calldata 'allowance(address,address)' "$EVM_CLIENT_ADDRESS" "$swap")" "$token")
}

read_state
if test "$send_balance" = "$zero" && test "$send_allowance" = "$zero" && \
   test "$client_balance" = "$zero" && test "$client_allowance" = "$zero"; then
  cast send --rpc-url "$rpc" --private-key "$EVM_SEND_PRIVATE_KEY" --value "$EVM_LIQUIDITY_WEI" "$token" 'deposit()' >/dev/null
  cast send --rpc-url "$rpc" --private-key "$EVM_SEND_PRIVATE_KEY" "$token" 'approve(address,uint256)' "$swap" "$max" >/dev/null
  cast send --rpc-url "$rpc" --private-key "$EVM_CLIENT_PRIVATE_KEY" --value "$EVM_CLIENT_LIQUIDITY_WEI" "$token" 'deposit()' >/dev/null
  cast send --rpc-url "$rpc" --private-key "$EVM_CLIENT_PRIVATE_KEY" "$token" 'approve(address,uint256)' "$swap" "$max" >/dev/null
elif ! test "$send_balance" = "$expected_send_balance" || ! test "$send_allowance" = "$max" || \
     ! test "$client_balance" = "$expected_client_balance" || ! test "$client_allowance" = "$max"; then
  printf >&2 'unexpected pre-existing EVM state: send_balance=%s send_allowance=%s client_balance=%s client_allowance=%s\n' \
    "$send_balance" "$send_allowance" "$client_balance" "$client_allowance"
  exit 1
fi

read_state
test "$send_balance" = "$expected_send_balance"
test "$client_balance" = "$expected_client_balance"
test "$send_allowance" = "$max"
test "$client_allowance" = "$max"

printf 'chain_id=%s\nswap_code_hash=%s\ntoken_code_hash=%s\nsend_balance=%s\nsend_allowance=%s\nclient_balance=%s\nclient_allowance=%s\n' \
  "$(cast chain-id --rpc-url "$rpc")" "$EVM_SWAP_CODE_HASH" "$EVM_TOKEN_CODE_HASH" \
  "$send_balance" "$send_allowance" "$client_balance" "$client_allowance"
