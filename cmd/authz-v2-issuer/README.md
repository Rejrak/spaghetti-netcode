# V2 certificate issuer API

Run `go run ./cmd/authz-v2-issuer` with these server-side environment variables:

- `SPAGHETTI_LISTEN_ADDR`, `SPAGHETTI_TLS_CERT_FILE`, `SPAGHETTI_TLS_KEY_FILE`
- `SPAGHETTI_KEYCLOAK_BASE_URL`, `SPAGHETTI_KEYCLOAK_REALM`, `SPAGHETTI_KEYCLOAK_CLIENT_ID`, `SPAGHETTI_KEYCLOAK_CLIENT_SECRET`, `SPAGHETTI_KEYCLOAK_AUDIENCE`
- `SPAGHETTI_POLICY_ID`, `SPAGHETTI_POLICY_VERSION`, `SPAGHETTI_POLICY_HASH` (64 lowercase hex digits), `SPAGHETTI_POLICY_SEND_PERMISSION`
- `SPAGHETTI_CHAIN_ID`, `SPAGHETTI_ISSUER_SET_ID`, `SPAGHETTI_ISSUER_IDS` (comma separated), `SPAGHETTI_ISSUER_SEED_FILES` (matching comma separated paths)
- `SPAGHETTI_ALPHAD_PATH`; optional `SPAGHETTI_ALPHA_HOME`, `SPAGHETTI_ALPHA_NODE`

Set `SPAGHETTI_KEYCLOAK_WALLET_ATTRIBUTE=1` when policy subjects use Keycloak `walletAddress` instead of usernames. Configure Keycloak to include the API audience in public-client access tokens. Issuer seed files contain hex Ed25519 seeds and must grant no group or world permissions. Configure trusted policy hash and issuer set to match Alpha. TLS is required by the command.

`POST https://<listen-address>/api/v2/certificates` accepts JSON with `subject`, `receiver`, `denom`, `amount`, `timeout_height`, `memo`, `fee_amount: [{denom, amount}]`, and `gas_limit`. All integer values are decimal strings. Send `Authorization: Bearer <Keycloak access token>`. The response contains V2 certificate protobuf bytes, signed intent, digest, height range, and authoritative account state. It never signs or broadcasts a user transaction.
