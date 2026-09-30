# Local Keycloak E2E

Disposable development-only Keycloak; never reuse these credentials elsewhere.

```sh
docker compose -f dev/keycloak/docker-compose.yml up -d
KEYCLOAK_TEST_USER_PASSWORD=local-console-only dev/keycloak/create-test-user.sh <live-cosmos-address>
```

Keycloak listens on `http://127.0.0.1:18080` by default. Override it with, for example, `KEYCLOAK_PORT=18081 docker compose -f dev/keycloak/docker-compose.yml up -d`.

Admin: `admin` / `local-admin-only`. Middleware client: `authz-middleware` / `local-authz-middleware-secret-do-not-reuse`, realm `alpha`.

Local console flow:

1. Start Keycloak with `docker compose -f dev/keycloak/docker-compose.yml up -d`. If an existing container already imported `alpha`, recreate that **development** container before using realm JSON changes; Keycloak skips startup import for an existing realm.
2. Run `dev/keycloak/create-test-user.sh <live-cosmos-address>`. The script creates or updates the user, keeps `authz-bank-send`, and sets a non-temporary password. Override the development-only default with `KEYCLOAK_TEST_USER_PASSWORD`.
3. Configure authz-console for realm `alpha`, public client `authz-console`, and Keycloak URL `http://127.0.0.1:18080`. Log in from `http://127.0.0.1:5173` or `http://localhost:5173` with the Cosmos address as username and the configured password. Use Authorization Code with PKCE S256. The public client has no secret.
4. The console receives an access token with `authz-middleware` in its `aud` claim. Configure issuer startup with `SPAGHETTI_KEYCLOAK_BASE_URL=http://127.0.0.1:18080` and `SPAGHETTI_KEYCLOAK_AUDIENCE=authz-middleware`. Use the same Keycloak hostname in browser login and issuer configuration so the `iss` claim matches. Leave `SPAGHETTI_KEYCLOAK_WALLET_ATTRIBUTE` unset.
5. The console sends `Authorization: Bearer <access-token>` to `POST /api/v2/certificates`. Middleware introspects the token and binds the Keycloak username to the requested Cosmos subject.

Use a same-origin HTTPS proxy for the issuer API if console and issuer have different origins. Do not put the `authz-middleware` client secret in the browser.

Stop the local realm with `docker compose -f dev/keycloak/docker-compose.yml down` after testing.
