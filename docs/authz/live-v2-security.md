# Live V2 RPC security evidence

Alpha's deterministic Ante harness (`alpha/app/v2_negative_paths_test.go`) decodes signed transactions and runs the installed AnteHandler in a cache context. It proves error classes and no cached state change. This command uses `broadcast_tx_sync` against a live Alpha node, then queries committed transactions and account state. CheckTx rejection means admission failed; it is **not** a committed transaction failure. The exact replay uses the same TxRaw bytes after the first transaction commits. Its expected rejection comes from the Cosmos SDK account sequence check, before V2 certificate verification.

Prerequisites: V2-enabled local Alpha with current issuer set 9 and matching demo issuer keys; funded local `alice` key in the test keyring; zero-fee local chain; `token` balance of at least 1 for Alice; Bob receiver address; `alphad` binary and its home. The command creates an in-memory signing account, funds it with exactly 1 `token` through a valid V2 transaction from Alice, and uses that account for all three adversarial sends. The isolated key is never written or printed. Demo issuer seed files must be outside the repository with mode `0600`.

```sh
GOTOOLCHAIN=go1.24.13 go run ./cmd/authz-v2-security-live \
  --alphad '<absolute alphad path>' --home '<Alpha home>' \
  --chain-id alpha-1 --node tcp://127.0.0.1:26657 \
  --alice alice --receiver '<bob cosmos address>' \
  --issuer-alpha-seed-file '<0600 alpha seed file>' \
  --issuer-beta-seed-file '<0600 beta seed file>'
```

Defaults match `authz-v2-demo`: policy `policy-bank-send`, version `1`, permission `supply.transaction.send`, issuer set `9`, denomination `token`, zero fee. Override policy fields with `--policy-id`, `--policy-version`, and `--permission` when the local trusted issuer configuration differs. The command broadcasts each transaction once; it never retries a rejected transaction. It waits for the funding and replay-control transactions to commit before continuing. It queries one later block to check rejected transaction absence. Live negative scenarios: `MALFORMED_CERTIFICATE`, `EXPIRED_CERTIFICATE`, `EXACT_RAW_TX_REPLAY`.

Output is one JSON document with public `sender_address`, `bootstrap`, `replay_control`, and three `scenarios`. Each observation includes `scenario`, `expected_layer`, `broadcast_count`, optional `tx_hash`, actual `checktx_code` when CheckTx ran, optional `checktx_codespace`/`checktx_log`/`observed_reason`, and committed fields only where applicable. `checktx_log` contains only a recognized error excerpt; raw RPC logs are omitted because they may echo submitted signatures. Replay fields compare sender sequence and token balances immediately before and after the second broadcast; `replay_created_second_commit=false` means the replay caused no second state transition. The replay hash identifies the already committed first transaction, so the command does not classify a hash query as a second commit. No key, mnemonic, seed, signature, or token is included in JSON.

CometBFT may keep committed transactions in its mempool cache. If the exact replay returns `tx already exists in cache`, the JSON records `observed_layer: "CometBFT mempool cache"`, omits `checktx_code`, and the command exits nonzero after checking that no second transfer occurred. That result does **not** prove SDK wrong-sequence enforcement. To observe SDK CheckTx for an exact replay, run a local node with the mempool transaction cache disabled before starting this harness; do not change Alpha consensus behavior.

Failure to obtain a CheckTx response, match the expected RPC hash, observe a new block, confirm the control transaction, or verify the one-transfer balance and sequence delta exits nonzero. Actual CheckTx codes and logs are reported; an expected V2 reason is never fabricated.
