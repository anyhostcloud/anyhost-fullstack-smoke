# anyhost-fullstack-smoke

Dockerfile-based golden app for the AnyHost **Fullstack Resource Gate**.

Endpoints:

| Path | Purpose |
| --- | --- |
| `GET /health` | Process is up |
| `GET /ready` | Postgres + storage + Redis connectivity |
| `GET /db` | Create/insert/read row via `DATABASE_URL` |
| `GET /storage` | Put/get object via task-role S3 (`S3_BUCKET` / `S3_PREFIX` / `S3_REGION`) |
| `GET /redis` | Set/get under `REDIS_KEY_PREFIX` via `REDIS_URL` |
| `GET /env` | Non-sensitive presence flags + optional plain marker / secret checksum |

Listens on port `8080`.

## Required managed resources

Provision before deploy:

```sh
anyhost db create -e dev --wait
anyhost storage create -e dev --wait
anyhost redis create -e dev --wait
anyhost context
anyhost deploy -e dev
```

Optional env markers for `/env` checks:

```text
SMOKE_PLAIN_MARKER=visible
SMOKE_SECRET_MARKER=<secret; only sha256 is exposed>
```

## Verify

```sh
BASE_URL=https://<public-url>

curl -fsS "$BASE_URL/health"
curl -fsS "$BASE_URL/ready"
curl -fsS "$BASE_URL/db"
curl -fsS "$BASE_URL/storage"
curl -fsS "$BASE_URL/redis"
curl -fsS "$BASE_URL/env"
```

## Local tests

```sh
go test ./...
```

## Publish to GitHub

See [`PUBLISH.md`](./PUBLISH.md). This directory is the source of truth until
`https://github.com/anyhostcloud/anyhost-fullstack-smoke` exists.
