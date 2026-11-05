.PHONY: setup sync sync-check restore-web build-web lint lint-install test build docker-build docker-up docker-down docker-save

GOLANGCI_LINT_VERSION ?= v2.12.2
GOLANGCI_LINT ?= $(shell command -v golangci-lint 2>/dev/null || echo "$(shell go env GOPATH)/bin/golangci-lint")

DOCKER_IMAGE ?= litepan-go:dev
DOCKER_PLATFORM ?= linux/amd64
DOCKER_EXPORT ?= dist/$(DOCKER_IMAGE).tar.gz

lint:
	@GOWORK=off "$(GOLANGCI_LINT)" run -c .golangci.yml ./...

lint-install:
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

test:
	@GOWORK=off go test -race ./...

build:
	@GOWORK=off go build -tags fuse ./...

build-nofuse:
	@GOWORK=off go build ./...

docker-build:
	@mkdir -p dist
	docker build --platform $(DOCKER_PLATFORM) -t $(DOCKER_IMAGE) .

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

docker-save: docker-build
	@mkdir -p dist
	docker save $(DOCKER_IMAGE) | gzip > $(DOCKER_EXPORT)

# ── 二开 / 上游同步（详见 docs/upstream-sync.md）─────────────────────────────

# 每个 clone 跑一次：启用 internal/api/web 的 merge=ours driver（配置不随仓库传播）
setup:
	@git config merge.ours.driver true
	@echo "merge.ours.driver enabled (internal/api/web conflicts resolve to local)"

# 合并上游更新：报告 → 确认 → 打退路 tag → 合并 → 清理产物 → make test
sync:
	@bash scripts/sync-upstream.sh

# 只看上游更新了什么，不做任何改动
sync-check:
	@bash scripts/sync-upstream.sh --check

# 丢弃本地前端构建产物改动（含未跟踪的新 hash 文件）；构建后不该提交它们
restore-web:
	@git restore -- internal/api/web 2>/dev/null || true
	@git clean -fdq -- internal/api/web
	@echo "internal/api/web restored (incl. untracked build residue)"

# 构建前端并还原产物，保持工作区干净
build-web:
	@cd web && npm run build
	@$(MAKE) --no-print-directory restore-web
