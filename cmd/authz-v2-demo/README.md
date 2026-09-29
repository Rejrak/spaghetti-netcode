# Local V2 one-transaction demo

Prerequisites: a running V2-enabled Alpha node (RPC `tcp://127.0.0.1:26657`),
an `alphad` binary/home with a funded `alice` key, current issuer set 9 containing
the two matching local demo issuer public keys, and a reachable Keycloak realm
where Alice has the configured send permission. Use a subject with **no V1
CURRENT authorization**. Keep the two issuer seed files outside this repository
with mode `0600`. This command does not create or publish a V1 grant.

Set the trusted gateway environment:

```sh
export SPAGHETTI_KEYCLOAK_BASE_URL=http://127.0.0.1:18080
export SPAGHETTI_KEYCLOAK_REALM=alpha
export SPAGHETTI_KEYCLOAK_CLIENT_ID=authz-middleware
export SPAGHETTI_KEYCLOAK_CLIENT_SECRET='<local development client secret>'
export SPAGHETTI_POLICY_ID=policy-bank-send
export SPAGHETTI_POLICY_VERSION=1
export SPAGHETTI_POLICY_SEND_PERMISSION=supply.transaction.send
```

Run from the middleware repository, replacing the bracketed local paths and
the addresses resolved from the current Alpha keyring:

```sh
GOTOOLCHAIN=go1.24.13 go run ./cmd/authz-v2-demo \
  --subject '<alice cosmos address>' --receiver '<bob cosmos address>' \
  --amount 1000 --account alice \
  --alphad '<absolute path to current alphad>' --home '<temporary Alpha home>' \
  --chain-id alpha-1 --keyring-backend test --node tcp://127.0.0.1:26657 \
  --issuer-alpha-seed-file '<0600 alpha seed file>' \
  --issuer-beta-seed-file '<0600 beta seed file>'
```

The default fee is empty (suitable only when the local node permits zero fees);
set `--fee-denom` and `--fee-amount` if that node requires a funded fee. The
command issues a height-bounded certificate off-chain, signs one direct
`MsgSend` with `SIGN_MODE_DIRECT`, broadcasts its `TxBytes` once, and waits for
an included `code=0` result. A stale sequence or rejected certificate is a
failure; rerun the whole command to obtain a new certificate.
