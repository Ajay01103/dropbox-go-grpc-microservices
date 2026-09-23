#!/usr/bin/env bash
set -euo pipefail

CONTAINER="scylladb-dev"

drop_all_tables() {
  local ks="$1"
  echo "=== Dropping all tables in ${ks} ==="
  podman exec -i "$CONTAINER" cqlsh -e "
    SELECT table_name FROM system_schema.tables WHERE keyspace_name = '${ks}';
  " | grep -v '^ *$' | tail -n +3 | awk '{print $1}' | while read -r table; do
    [ -z "$table" ] && continue
    echo "DROP TABLE IF EXISTS ${ks}.${table};"
    podman exec -i "$CONTAINER" cqlsh -e "DROP TABLE IF EXISTS ${ks}.${table};"
  done
  echo "=== Done: ${ks} ==="
}

drop_all_tables "metadata_ks"
drop_all_tables "upload_ks"
