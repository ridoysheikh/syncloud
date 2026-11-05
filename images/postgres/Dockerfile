# SynCloud managed PostgreSQL (Phase 13): one image for every role of a
# cluster — Patroni-managed Postgres, PgBouncer and WAL-G.
#
# Base: official Postgres (Debian bookworm, PGDG) with pg_duckdb; one image
# per major version (PG_MAJOR: 17 or 18). Extensions are permissively
# licensed; TimescaleDB is the Apache-only ("oss") build, which may be
# offered as a database service.
ARG PG_MAJOR=18
FROM pgduckdb/pgduckdb:${PG_MAJOR}-v1.1.1
ARG PG_MAJOR
LABEL org.opencontainers.image.source="https://github.com/ridoysheikh/syncloud" \
      org.opencontainers.image.description="SynCloud managed PostgreSQL: Patroni, PgBouncer, WAL-G and extensions" \
      org.opencontainers.image.licenses="Apache-2.0"

ARG WALG_VERSION=v3.0.9
ARG ETCD_VERSION=v3.7.2
ARG TARGETARCH=amd64

USER root
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl gnupg; \
    # TimescaleDB (Apache-only build) from Timescale's repository.
    curl -fsSL https://packagecloud.io/timescale/timescaledb/gpgkey | gpg --dearmor -o /usr/share/keyrings/timescaledb.gpg; \
    echo "deb [signed-by=/usr/share/keyrings/timescaledb.gpg] https://packagecloud.io/timescale/timescaledb/debian/ bookworm main" > /etc/apt/sources.list.d/timescaledb.list; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        postgresql-${PG_MAJOR}-pgvector \
        postgresql-${PG_MAJOR}-partman \
        postgresql-${PG_MAJOR}-cron \
        postgresql-${PG_MAJOR}-hypopg \
        postgresql-${PG_MAJOR}-postgis-3 \
        timescaledb-2-oss-postgresql-${PG_MAJOR} \
        patroni python3-etcd python3-psycopg2 \
        pgbouncer; \
    # WAL-G (Apache 2): continuous archiving and backups to S3.
    # WAL-G names arm64 builds "aarch64".
    walg_arch=$([ "${TARGETARCH}" = arm64 ] && echo aarch64 || echo "${TARGETARCH}"); \
    curl -fsSL -o /usr/local/bin/wal-g "https://github.com/wal-g/wal-g/releases/download/${WALG_VERSION}/wal-g-pg-22.04-${walg_arch}"; \
    chmod 0755 /usr/local/bin/wal-g; \
    wal-g --version; \
    # etcd (Apache 2): the platform's coordination store for Patroni. It
    # ships in this image so its members have a shell to wait for DNS.
    curl -fsSL "https://github.com/etcd-io/etcd/releases/download/${ETCD_VERSION}/etcd-${ETCD_VERSION}-linux-${TARGETARCH}.tar.gz" \
      | tar -xz -C /usr/local/bin --strip-components=1 "etcd-${ETCD_VERSION}-linux-${TARGETARCH}/etcd" "etcd-${ETCD_VERSION}-linux-${TARGETARCH}/etcdctl"; \
    etcd --version | head -1; \
    apt-get purge -y gnupg; apt-get autoremove -y; \
    rm -rf /var/lib/apt/lists/*; \
    mkdir -p /data /etc/syncloud; chown -R postgres:postgres /data /etc/syncloud

# `app` is not a superuser, so CREATE EXTENSION would fail for every extension
# PostgreSQL doesn't mark "trusted" (vector, PostGIS, pg_cron, ...). Trusting
# the platform's list lets the database's owner run CREATE EXTENSION in the
# console or any client, as with a hosted database's master user. Anything not
# on this list stays superuser-only. Keep in step with pgExtensionAllowList in
# internal/dbs/pgprivs.go (a test checks it).
ARG TRUSTED_EXTENSIONS="vector timescaledb pg_duckdb postgis postgis_topology postgis_raster postgis_tiger_geocoder pg_partman pg_cron hypopg pg_stat_statements pg_trgm pgcrypto hstore btree_gin btree_gist postgres_fdw uuid-ossp tablefunc citext ltree fuzzystrmatch unaccent intarray cube earthdistance pg_buffercache pgstattuple pg_prewarm amcheck dblink tsm_system_rows tsm_system_time isn seg bloom pg_visibility plpgsql"
RUN set -eu; \
    dir=/usr/share/postgresql/${PG_MAJOR}/extension; \
    for e in ${TRUSTED_EXTENSIONS}; do \
      f="$dir/$e.control"; \
      [ -f "$f" ] || { echo "no control file for $e" >&2; exit 1; }; \
      if grep -q '^trusted' "$f"; then sed -i 's/^trusted.*/trusted = true/' "$f"; else printf '\ntrusted = true\n' >> "$f"; fi; \
    done

COPY entrypoint.sh /usr/local/bin/syncloud-pg
COPY walg-bootstrap walg-adopt walg-restore-env walg-restore-wal walg-replica /usr/local/bin/
RUN chmod 0755 /usr/local/bin/syncloud-pg /usr/local/bin/walg-bootstrap /usr/local/bin/walg-adopt /usr/local/bin/walg-restore-wal /usr/local/bin/walg-replica

USER postgres
ENV PATH=/usr/lib/postgresql/${PG_MAJOR}/bin:$PATH
ENTRYPOINT ["/usr/local/bin/syncloud-pg"]
CMD ["patroni"]
