# V2 middleware observability contract

This middleware-only contract extends the shared
[observability contract](observability-contract.md) without changing Alpha's
on-chain event schema. These phase events are a stable integration boundary
for later consumers; they are not an authorization authority.

The V2 middleware emits one structured log per reached phase. `subject` is
the canonical Cosmos account address, not a Keycloak username or raw attribute.
`certificate_digest` is lowercase hex SHA-256 of canonical issuer sign bytes;
`tx_hash` is SHA-256 of the final signed `TxRaw` bytes (uppercase hex from the
current Comet broadcaster). Consumers
must not infer chain inclusion from `v2_tx_broadcast`: only
`v2_tx_confirmed` means included with code zero.

| Event | Stable correlation fields |
| --- | --- |
| `v2_policy_evaluated` | `subject`, `outcome` (`allow`/`deny`), `reason_code`, `policy_id`, `policy_version` (evaluator string) |
| `v2_certificate_built` | `subject`, `sequence`, `policy_id`, `policy_version`, `issuer_set_id`, `certificate_digest` |
| `v2_certificate_signed` | preceding certificate fields plus `signature_count` |
| `v2_tx_built` | `certificate_digest`, `tx_hash`, `subject`, `sequence`, `policy_id`, `policy_version`, `issuer_set_id`, `signature_count` |
| `v2_tx_broadcast` | same transaction correlation fields; sync CheckTx accepted, not yet included |
| `v2_tx_confirmed` | same transaction correlation fields plus `height` and `code=0` |
| `v2_tx_failed` | available transaction correlation fields plus `phase` (`build`, `broadcast`, `confirmation`); `height` and `code` when confirmation returned them |

The policy event precedes trusted account-state lookup, so it has no sequence
or certificate digest. `tx_hash` is absent before a signed TxRaw exists.
Failures before certificate issuance are represented by the policy event or
returned error, not by a fabricated digest/hash. `v2_tx_failed` is never a
successful inclusion signal. No event contains Keycloak client secrets, issuer
seeds/private keys, raw signatures/certificate/transaction bytes, or policy
attribute contents. The later UI may consume these fields; it is not built here.
