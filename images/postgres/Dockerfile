# SynCloud managed PostgreSQL (Phase 13): one image for every role of a
# cluster — Patroni-managed Postgres, PgBouncer and WAL-G.
#
# Base: official Postgres 17 (Debian bookworm, PGDG) with pg_duckdb.
# Extensions are permissively licensed; TimescaleDB is the Apache-only
# ("oss") build, which may be offered as a database service.
FROM pgduckdb/pgduckdb:17-v1.1.1

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
        postgresql-17-pgvector \
        postgresql-17-partman \
        postgresql-17-cron \
        postgresql-17-hypopg \
        postgresql-17-postgis-3 \
        timescaledb-2-oss-postgresql-17 \
        patroni python3-etcd python3-psycopg2 \
        pgbouncer; \
    # WAL-G (Apache 2): continuous archiving and backups to S3.
    curl -fsSL -o /usr/local/bin/wal-g "https://github.com/wal-g/wal-g/releases/download/${WALG_VERSION}/wal-g-pg-22.04-${TARGETARCH}"; \
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

COPY entrypoint.sh /usr/local/bin/syncloud-pg
RUN chmod 0755 /usr/local/bin/syncloud-pg

USER postgres
ENV PATH=/usr/lib/postgresql/17/bin:$PATH
ENTRYPOINT ["/usr/local/bin/syncloud-pg"]
CMD ["patroni"]
