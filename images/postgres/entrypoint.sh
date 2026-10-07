#!/bin/sh
# Starts one role of a SynCloud PostgreSQL cluster. The controller renders
# every configuration file and passes it base64-encoded in the environment,
# so this script holds no logic of its own.
#
#   patroni    Postgres managed by Patroni   ($PATRONI_CONFIG_B64)
#   pgbouncer  connection pooler              ($PGBOUNCER_INI_B64, $PGBOUNCER_USERS_B64)
#   anything else runs as given (wal-g, psql, …)
set -eu
umask 077
write() { # file, base64 content
  printf '%s' "$2" | base64 -d > "$1"
}
case "${1:-patroni}" in
  patroni)
    write /etc/syncloud/patroni.yml "$PATRONI_CONFIG_B64"
    mkdir -p /data/pgdata /data/run && chmod 0700 /data/pgdata /data/run
    exec patroni /etc/syncloud/patroni.yml
    ;;
  etcd)
    # Peers are named, not addressed: wait until this member's own name
    # resolves (discovery publishes it once the container has an address).
    until getent hosts "$ETCD_SELF" >/dev/null; do echo "waiting for $ETCD_SELF"; sleep 1; done
    shift
    exec etcd "$@"
    ;;
  pgbouncer)
    write /etc/syncloud/pgbouncer.ini "$PGBOUNCER_INI_B64"
    write /etc/syncloud/userlist.txt "$PGBOUNCER_USERS_B64"
    exec pgbouncer /etc/syncloud/pgbouncer.ini
    ;;
  *)
    exec "$@"
    ;;
esac
