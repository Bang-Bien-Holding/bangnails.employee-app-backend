# bangnails.employee-app-backend

Backend API for the Bang Nails employee app: employee/store management, Odoo
sync, and presence-verified login sessions.

For domain concepts and terminology, see [CONTEXT.md](CONTEXT.md). For
architectural decisions, see [docs/adr/](docs/adr/).

## Prerequisites

- Go 1.25.12 (see `go.mod`)
- Docker (for Postgres and Mailpit via `docker-compose.yaml`)
- [`sqlc`](https://docs.sqlc.dev/) and [`goose`](https://github.com/pressly/goose), for the database steps below:

  ```sh
  go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
  go install github.com/pressly/goose/v3/cmd/goose@latest
  ```

## Local setup

1. Copy `.env` and fill in the required values (Postgres credentials, Odoo
   API credentials, mail settings — see the variable names in `.env` for
   the full list).

2. Start Postgres and Mailpit:

   ```sh
   docker compose up -d
   ```

3. Run database migrations:

   ```sh
   goose -dir internal/adapters/postgresql/migrations up
   ```

   Migrations use the `GOOSE_DBSTRING`, `GOOSE_DRIVER`, and
   `GOOSE_MIGRATION_DIR` variables from `.env`.

4. Run the server:

   ```sh
   go run ./cmd
   ```

   The server listens on `ADDR` (from `.env`); the API is versioned under
   `/v1`.

## Database migrations & sqlc codegen

New migrations go in `internal/adapters/postgresql/migrations/`, numbered
sequentially (`NNNNN_description.sql`), and are applied with:

```sh
goose -dir internal/adapters/postgresql/migrations up
```

After changing a query in `internal/adapters/postgresql/sqlc/queries.sql`
or the schema, regenerate the sqlc-generated code:

```sh
sqlc generate
```

This regenerates `internal/adapters/postgresql/sqlc/queries.sql.go`,
`querier.go`, and the `mocks/mock_querier.go` mock — commit all of them
together with the query/schema change.

## Running tests

Unit tests (no external dependencies):

```sh
go test -race -count=1 ./...
```

End-to-end tests (build tag `dbe2e`) run against a real Postgres instance
and require migrations to already be applied:

```sh
docker compose up -d
goose -dir internal/adapters/postgresql/migrations up
go test -tags dbe2e -race -count=1 ./cmd/...
```

e2e tests also construct an `odoo.Client` on startup even when a given
suite never calls Odoo, so `ODOO_BASE_URL`, `ODOO_CLIENT_ID`,
`ODOO_CLIENT_SECRET`, `ODOO_USERNAME`, `ODOO_PASSWORD`, and
`ODOO_DATABASE` must all be set to *some* value (see the `dbe2e` job in
`.github/workflows/backend-ci.yml` for the values CI uses when it isn't
exercising real Odoo behavior).

## Deployment / Docker build

The `Dockerfile` builds a static binary and runs it from a `scratch` base
image:

```sh
docker build -t bangnails-employee-backend .
docker run -p 8080:8080 --env-file .env bangnails-employee-backend
```

CI (`.github/workflows/backend-ci.yml`) runs lint, build, unit tests, and
the `dbe2e` suite (with migrations applied against a fresh Postgres) on
every PR into `main`, and again on push to `main`/`staging`.
