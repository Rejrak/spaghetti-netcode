#!/usr/bin/env bash

main() {
  local address="${1:-${COSMOS_ADDRESS:-}}"
  local script_dir config_file query_output user_id
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  config_file="/tmp/kcadm-authz-e2e.config"

  if [[ ! "$address" =~ ^cosmos1[023456789acdefghjklmnpqrstuvwxyz]+$ ]]; then
    printf 'usage: %s <canonical-cosmos-address>\n' "$0" >&2
    return 2
  fi

  if ! docker compose -f "$script_dir/docker-compose.yml" exec -T keycloak \
    /opt/keycloak/bin/kcadm.sh config credentials \
    --server http://localhost:8080 \
    --realm master \
    --user admin \
    --password local-admin-only \
    --config "$config_file" >/dev/null; then
    printf 'failed to authenticate to local Keycloak\n' >&2
    return 1
  fi

  if ! query_output="$(docker compose -f "$script_dir/docker-compose.yml" exec -T keycloak \
    /opt/keycloak/bin/kcadm.sh get users \
    -r alpha \
    -q "username=$address" \
    -q exact=true \
    --fields id \
    --format csv \
    --noquotes \
    --config "$config_file" 2>/dev/null)"; then
    docker compose -f "$script_dir/docker-compose.yml" exec -T keycloak \
      rm -f "$config_file" >/dev/null 2>&1
    printf 'failed to query local Keycloak test user\n' >&2
    return 1
  fi
  query_output="${query_output//$'\r'/}"
  user_id="${query_output%%$'\n'*}"

  if [[ -z "$user_id" ]]; then
    user_id="$(docker compose -f "$script_dir/docker-compose.yml" exec -T keycloak \
      /opt/keycloak/bin/kcadm.sh create users \
      -r alpha \
      -s "username=$address" \
      -s enabled=true \
      -i \
      --config "$config_file")" || {
        docker compose -f "$script_dir/docker-compose.yml" exec -T keycloak \
          rm -f "$config_file" >/dev/null 2>&1
        printf 'failed to create local Keycloak test user\n' >&2
        return 1
      }
  else
    if ! docker compose -f "$script_dir/docker-compose.yml" exec -T keycloak \
      /opt/keycloak/bin/kcadm.sh update "users/$user_id" \
      -r alpha \
      -s "username=$address" \
      -s enabled=true \
      --config "$config_file" >/dev/null; then
      docker compose -f "$script_dir/docker-compose.yml" exec -T keycloak \
        rm -f "$config_file" >/dev/null 2>&1
      printf 'failed to update local Keycloak test user\n' >&2
      return 1
    fi
  fi

  if ! docker compose -f "$script_dir/docker-compose.yml" exec -T keycloak \
    /opt/keycloak/bin/kcadm.sh add-roles \
    -r alpha \
    --uid "$user_id" \
    --rolename authz-bank-send \
    --config "$config_file" >/dev/null; then
    docker compose -f "$script_dir/docker-compose.yml" exec -T keycloak \
      rm -f "$config_file" >/dev/null 2>&1
    printf 'failed to assign authz-bank-send to local Keycloak test user\n' >&2
    return 1
  fi

  docker compose -f "$script_dir/docker-compose.yml" exec -T keycloak \
    rm -f "$config_file" >/dev/null 2>&1
  printf 'local Keycloak user ready: %s\n' "$address"
}

main "$@"
