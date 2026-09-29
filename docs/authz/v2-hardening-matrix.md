# V2 final hardening matrix

This maps each threat to a concrete deterministic test. Middleware tests run in
this repository. Chain-authoritative rows map to Alpha's V2 tests on
`service-manager` (reviewed at `5993752cda4abfa61cbb5c925c991c37702ea031`);
they are not replaced by middleware mocks. The separate local live observation
is in [v2-live-e2e.md](v2-live-e2e.md); phase-log fields are in the
[V2 middleware observability contract](v2-middleware-observability-contract.md).

| Threat / failure | Concrete test and boundary |
| --- | --- |
| Policy deny | Middleware `TestCertificateIssuerV2FailClosed/policy_deny`: no certificate or state query; `TestV2OneTxFlowFailureBoundaries/issuance_denied`: no build/broadcast. |
| Wrong policy metadata | Middleware `TestCertificateIssuerV2FailClosed/policy_ID_mismatch`, `/policy_version_mismatch`, `/noncanonical_policy_version`; Alpha `TestVerifyCertificateV2/tampered_policy_hash`. |
| Wrong chain | Middleware `TestCertificateIssuerV2FailClosed/wrong_chain`; Alpha `TestV2TransactionIntentAndShapeFailures/wrong_chain`. |
| Wrong account number | Middleware `TestBuildSignedV2Transaction` verifies SIGN_MODE_DIRECT fails with another account number; Alpha `TestV2TransactionIntentAndShapeFailures/wrong_account_number`. |
| Stale sequence | Middleware `TestBuildSignedV2TransactionRejectsWrongSignerAndMetadata` disallows local sequence rewrite; `TestV2OneTxFlowFailureBoundaries/stale_sequence` sends once; Alpha `TestV2TransactionIntentAndShapeFailures/wrong_sequence`. |
| Wrong account signer | Middleware `TestBuildSignedV2TransactionRejectsWrongSignerAndMetadata` and `TestV2OneTxFlowFailureBoundaries/wrong_account_signer`: no broadcast. |
| Tampered MsgSend | Middleware `TestBuildSignedV2Transaction/MsgSend` invalidates account signature; Alpha `TestV2TransactionIntentAndShapeFailures/wrong_receiver`, `/wrong_denom`, `/wrong_amount`. |
| Tampered memo / fee / gas / timeout | Middleware `TestBuildSignedV2Transaction/{memo,fee,gas,timeout}` invalidates account signature; Alpha `TestV2TransactionIntentAndShapeFailures/{wrong_memo,wrong_fee,wrong_gas,wrong_timeout}` rejects issuer-intent mismatch. |
| Tampered certificate | Middleware `TestBuildSignedV2Transaction/certificate` invalidates account signature and `TestV2OneTxFlowFailureBoundaries/tampered_certificate_after_issuance` blocks before broadcast. |
| Bad issuer signature | Alpha `TestVerifyCertificateV2/bad_extra_after_quorum`, `TestV2TransactionIntentAndShapeFailures/bad_issuer_signature`; middleware flow propagates a CheckTx rejection in `TestV2OneTxFlowFailureBoundaries/bad_issuer_signature`. |
| Insufficient quorum | Alpha `TestVerifyCertificateV2/insufficient_quorum`. Middleware does not calculate chain quorum. |
| Expired / not-yet-valid certificate | Alpha `TestVerifyCertificateV2/{expired,not_yet_valid}` and `TestV2FailureEmitsNoSuccessEvent/expired`; middleware flow propagates rejection in `TestV2OneTxFlowFailureBoundaries/expired_certificate`. |
| Stale issuer set | Alpha `TestVerifyCertificateV2/stale_current_selection` and `TestV2FailureEmitsNoSuccessEvent/rotated_issuer_set`. |
| Duplicate issuer signature | Alpha `TestVerifyCertificateV2/duplicate_issuer` and `TestCertificateStructureV2/duplicate_issuer`. |
| Malformed / unknown protobuf fields | Alpha `TestV2RawStrictness` covers malformed certificate and unknown certificate, sign-doc, intent, coin, signature, message, body, auth-info and raw fields; middleware `TestSDKV2CertificateSchemaParity` plus `pb/sdkv2/check-generated.sh` detect schema/generator drift. |
| Broadcast CheckTx failure | Middleware `TestV2TransportRejectsFailures/CheckTx_failure` and `TestV2OneTxFlowFailureBoundaries/broadcast_failure`: no confirmation. |
| Broadcast hash mismatch | Middleware `TestV2TransportRejectsFailures/hash_mismatch` and `TestV2OneTxFlowFailureBoundaries/broadcast_wrong_hash`: no confirmation. |
| Confirmation wrong tx bytes / hash | Middleware `TestV2TransportRejectsFailures/{wrong_tx_bytes,wrong_hash}`; confirmer checks both response hash and SHA-256 of included bytes. |
| Inclusion code != 0 | Middleware `TestV2TransportRejectsFailures/included_failure` and `TestV2OneTxFlowFailureBoundaries/included_nonzero_code`: no confirmed event. |
| No retry / no second broadcast | Middleware `TestV2OneTxFlowSuccess` asserts exactly one issue/build/broadcast/confirm; `TestV2OneTxFlowFailureBoundaries` asserts call counts on every failure. |
| Context cancellation | Middleware `TestV2TransportRejectsFailures` rejects pre-cancelled broadcast/confirmation with zero RPC calls; `TestAlphadAccountStateProviderV2RejectsUntrustedResponses` rejects pre-cancelled state query. |
| Omitted protobuf-zero account number / sequence | Middleware `TestAlphadAccountStateProviderV2OmittedZeroFields`; `TestAlphadAccountStateProviderV2RejectsUntrustedResponses` retains strict rejection of noncanonical present values and duplicate keys. |
| Secret-bearing observability | Middleware `TestCertificateIssuerV2LogsOnlyPublicCorrelation`, `TestV2OneTxFlowSuccess`, and `TestV2DemoConfiguration` check attributes, test issuer key material, certificate/signature bytes and configured Keycloak client-secret marker stay out of logs/output. |

These are defense boundaries, not synthetic replacement for Alpha's keeper and
Ante checks. No V1 CURRENT/batch operation is invoked in the V2 one-transaction
path; the local E2E additionally observed `CURRENT found=false` before/after.

`BenchmarkV1CanonicalBatchSignBytes` and the `BenchmarkV2*` functions are
in-process microbenchmarks (`go test ./internal/authorization -run '^$' -bench 'Benchmark(V1|V2)' -benchmem`), not the live chain E2E measurement. V1 batch
and V2 certificate shapes differ, so their numbers are diagnostic rather than
an end-to-end latency ratio.
