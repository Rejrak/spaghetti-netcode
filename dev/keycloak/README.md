# Local Keycloak E2E

Disposable development-only Keycloak; never reuse these credentials elsewhere.

```sh
docker compose -f dev/keycloak/docker-compose.yml up -d
dev/keycloak/create-test-user.sh <live-cosmos-address>
docker compose -f dev/keycloak/docker-compose.yml down
```

Keycloak listens on `http://127.0.0.1:18080` by default. Override it with, for example, `KEYCLOAK_PORT=18081 docker compose -f dev/keycloak/docker-compose.yml up -d`.

Admin: `admin` / `local-admin-only`. Middleware client: `authz-middleware` / `local-authz-middleware-secret-do-not-reuse`, realm `alpha`.
