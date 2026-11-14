# Job scraper - common tasks.
# Works with GNU make and mingw32-make, in PowerShell or Git Bash.
# Only `go` commands are used so it does not depend on Unix tools.
#
# On Windows you only have mingw32-make; run:  mingw32-make <target>
# Or in PowerShell, once per session:          Set-Alias make mingw32-make

.DEFAULT_GOAL := help
.PHONY: help build test test-unit test-web serve vet fmt tidy check ping migrate migrate-down migrate-status stats seed run run-all run-due sources enable disable renormalize dedup quality seed-locations prune version

SOURCE ?=

help:
	@echo Job scraper targets
	@echo Dev:
	@echo     build            compile to bin/
	@echo     test             run all tests, including database integration tests
	@echo     test-unit        fast Go tests only, no database
	@echo     test-web         dashboard helper tests, needs Node
	@echo     vet              static checks
	@echo     fmt              format code
	@echo     tidy             tidy go.mod and go.sum
	@echo     check            fmt, vet, test and build in one go
	@echo Database, needs .env with DATABASE_URL:
	@echo     ping             check the database connection
	@echo     migrate          apply pending migrations
	@echo     migrate-status   show applied migrations
	@echo     migrate-down     roll back the last migration
	@echo     stats            counts, per-source status and database size
	@echo Scraping:
	@echo     seed             register sources from seeds/sources.txt
	@echo     run SOURCE=greenhouse:groww   scrape one source
	@echo     run-due          scrape only sources that are due, what the daily cron runs
	@echo     run-all          scrape every enabled source, due or not
	@echo Dashboard and API:
	@echo     serve            run the dashboard and API at http://127.0.0.1:8080
	@echo Source health:
	@echo     sources          list sources with state, failures and last run
	@echo     enable SOURCE=kind:token    re-enable a source
	@echo     disable SOURCE=kind:token   stop scraping a source
	@echo Data quality:
	@echo     renormalize      re-derive jobs from stored raw payloads, no scraping, then dedup
	@echo     dedup            merge identical jobs
	@echo     quality          coverage report for location, remote, seniority, experience, salary
	@echo     seed-locations   load reference cities and aliases
	@echo Planned, not implemented yet:
	@echo     prune            prune old raw payloads, phase 6
	@echo Other:
	@echo     version          print scraper version

build:
	go build -o bin/ ./cmd/scraper

test:
	go test ./...

test-unit:
	go test -short ./...

test-web:
	node --test public/lib.test.mjs

serve:
	go run ./cmd/serve

vet:
	go vet ./...

fmt:
	go fmt ./...

tidy:
	go mod tidy

check: fmt vet test test-web build

ping:
	go run ./cmd/scraper ping

migrate:
	go run ./cmd/scraper migrate

migrate-status:
	go run ./cmd/scraper migrate-status

migrate-down:
	go run ./cmd/scraper migrate-down

stats:
	go run ./cmd/scraper stats

seed:
	go run ./cmd/scraper seed

run:
	go run ./cmd/scraper run --source $(SOURCE)

run-all:
	go run ./cmd/scraper run --all

run-due:
	go run ./cmd/scraper run-due

sources:
	go run ./cmd/scraper sources

renormalize:
	go run ./cmd/scraper renormalize

dedup:
	go run ./cmd/scraper dedup

quality:
	go run ./cmd/scraper quality

seed-locations:
	go run ./cmd/scraper seed-locations

enable:
	go run ./cmd/scraper enable $(SOURCE)

disable:
	go run ./cmd/scraper disable $(SOURCE)

prune:
	go run ./cmd/scraper prune

version:
	go run ./cmd/scraper version
