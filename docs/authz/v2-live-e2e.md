# V2 local live E2E — 2026-09-29

This was a **local deterministic development-chain experiment**, not a
public-network transaction or production activation.

| Evidence | Observed value |
| --- | --- |
| Alpha commit | `92b525a1376d3ac70cb494888ca8e354134b808c` |
| Middleware commit (including account-state zero-field fix) | `22cbaae1b2339bfec641437a36236443cf301d9f` |
| Chain / subject | `alpha-1` / `cosmos1fwha2mty0wpeh9s0hh8em9ssmcmqk7p8hnzv9x` |
| Issuers | set 9, two issuer signatures |
| Certificate digest | `bf4b30cde59b7be5739a5a829930a6ed50e5955beedfda08eaa15ab88cf9bcbc` |
| Signed transaction hash | `AA77ACF7F149DDC3CF284366AD008D30BED196F3536419C508EADA07A38E098C` |
| Inclusion | height 947, code 0 |
| Account sequence | 1 → 2 |
| Alice `token` | 5,000,000 → 4,999,000 (−1,000) |
| Bob `token` | 50,000,000 → 50,001,000 (+1,000) |
| Cosmos transactions broadcast by this flow | exactly 1 |
| V1 `AuthorizationRecord CURRENT` | `found=false` before and after |

The certificate was issued off-chain. The sole broadcast was the final direct
`MsgSend` carrying the critical V2 certificate and a normal account
`SIGN_MODE_DIRECT` signature. No preliminary V1 batch transaction or CURRENT
write occurred. These measurements are an E2E observation, not a benchmark.
