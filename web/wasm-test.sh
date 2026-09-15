#!/bin/sh
set -eu
# wasm_exec.js has a small initial argv/environment region. The Nix development
# shell's complete environment exceeds it. Tests require only PATH, not the
# caller's environment (especially not a runtime wallet key).
exec env -i PATH="$PATH" node --stack-size=8192 "$("${GO:-go}" env GOROOT)/lib/wasm/wasm_exec_node.js" "$@"
