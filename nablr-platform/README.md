# nablr-platform

The Nablr payments platform: the Go API, the React web app, and the contracts
they share. One repository, because a payment feature is never only backend or
only frontend — the API change, the client change and the contract test that
proves they agree belong in one reviewable commit.

    apps/api        Go 1.23 · Gin · pgx · PostgreSQL. The modular monolith.
    apps/web        React 19 · Vite · TanStack Query · React Router · shadcn/ui · axios
    packages/       Shared across the web app: generated API client, UI kit
    docs/           Architecture decisions, threat model, runbook index

Infrastructure lives in `usenablr/nablr-infra`. The mobile app lives in
`usenablr/nablr-mobile`.

## Getting set up

See `docs/development.md`. Nothing here runs against production data, ever.

## Branching

`main` is always deployable and is the only long-lived branch. Work happens on
short-lived branches off `main` and returns by pull request:

    feat/transfers-idempotency-key
    fix/bill-reversal-double-credit
    chore/bump-pgx

Sandbox and production are **environments, not branches**. The same commit is
promoted from one to the other, so what you tested is what ships. A long-lived
`develop` or `staging` branch drifts from `main`, and in a payments system that
drift is where the incident comes from.





Godotenv: https://github.com/joho/godotenv
Go Viper: https://github.com/spf13/viper
docker: https://www.docker.com/
Go - https://go.dev/
Migrate - https://github.com/golang-migrate/mig...
SQLC - https://docs.sqlc.dev/en/latest/overv...


go install github.com/air-verse/air@latest
export PATH="$(go env GOPATH)/bin:$PATH"

sqlc generate