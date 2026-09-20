# Local Keycloak E2E

Disposable development-only Keycloak; never reuse these credentials elsewhere.

```sh
docker compose -f dev/keycloak/docker-compose.yml up -d
dev/keycloak/create-test-user.sh <live-cosmos-address>
docker compose -f dev/keycloak/docker-compose.yml down
```

Admin: `admin` / `local-admin-only`. Middleware client: `authz-middleware` / `local-authz-middleware-secret-do-not-reuse`, realm `alpha`.
