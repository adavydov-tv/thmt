.PHONY: help deps db db-down migrate run dev-front build build-front build-back collect google-auth fmt vet test clean

BIN := bin/server
GO  ?= go

help: ## Показать список команд
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

deps: ## Подтянуть зависимости Go и npm
	$(GO) mod tidy
	cd web && npm install

db: ## Поднять PostgreSQL в docker
	docker compose up -d postgres

db-down: ## Остановить PostgreSQL
	docker compose down

migrate: ## Применить схему БД и выйти
	$(GO) run ./cmd/server -migrate

run: ## Запустить бэкенд (API на :8080)
	$(GO) run ./cmd/server

dev-front: ## Запустить фронтенд в dev-режиме (Vite на :5173, проксирует /api)
	cd web && npm run dev

build-front: ## Собрать фронтенд в web/dist
	cd web && npm install && npm run build

build-back: ## Собрать бинарник в bin/server
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/server

build: build-front build-back ## Полная сборка

collect: ## Разовый сбор из CLI: make collect PERSON=adavydov FROM=2026-07-01 TO=2026-08-14
	$(GO) run ./cmd/server -collect -person=$(PERSON) -from=$(FROM) -to=$(TO)

fmt: ## Форматирование Go-кода
	gofmt -w ./cmd ./internal

vet: ## Статический анализ
	$(GO) vet ./...

test: ## Тесты
	$(GO) test ./...

clean: ## Удалить артефакты сборки
	rm -rf bin web/dist

google-auth: ## Получить GOOGLE_OAUTH_REFRESH_TOKEN через браузер
	$(GO) run ./cmd/googleauth
