# 服务名同时决定二进制和发布包名称。
APP := api
BIN := bin/$(APP)
VERSION ?= dev
PACKAGE := dist/$(APP)-$(VERSION).tar.gz
LDFLAGS ?= -s -w -X main.buildVersion=$(VERSION)
DOCKER_COMPOSE ?= docker compose
# 迁移夹具仅接受 _test 结尾的独占库，已有目标表会被拒绝而非清空。
INTEGRATION_MYSQL_DSN ?= root:password@tcp(127.0.0.1:3311)/api_test?charset=utf8mb4&parseTime=true&loc=Local
INTEGRATION_WAIT_TIMEOUT ?= 120
PROMTOOL_IMAGE ?= prom/prometheus:v2.55.1
PROMETHEUS_RULES := $(wildcard docs/prometheus/*.yml)
PROMETHEUS_RULES_IN_CONTAINER := $(patsubst docs/prometheus/%,/rules/%,$(PROMETHEUS_RULES))
GOVULNCHECK_VERSION ?= v1.6.0
GO_TOOLCHAIN ?= go1.26.6

# 仅打包固定二进制与公开模板，不递归包含本地运行配置或构建目录残留。
PACKAGE_FILES := bin/api bin/api-migrate etc/config.sample.yaml etc/config.dnmp.sample.yaml \
	deploy/docker/Dockerfile deploy/systemd/api.service deploy/integration/docker-compose.yml \
	docs/prometheus/api-alerts.yml docs/grafana/api-dashboard.json \
	docs/site/角色文档/运维/部署发布指南.md docs/site/角色文档/运维/数据库迁移治理.md README.md

.PHONY: fmt fmt-check test test-race vet build build-tools package check ci diff-check branch-drift-check secret-scan promtool-check govulncheck security-scan integration-env-up integration-env-down integration-test integration-test-run migrate-status migrate-dry-run migrate-bootstrap clean

# fmt 原地格式化项目自有 Go 文件。
fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

# fmt-check 只检查格式，不修改工作区。
fmt-check:
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*'))"

# test 运行全部默认 Go 测试。
test:
	go test ./...

# test-race 在竞态检测器下重新运行测试。
test-race:
	go test -race -count=1 ./...

# vet 执行 Go 官方静态检查。
vet:
	go vet ./...

# build 生成前台 API 服务二进制。
build:
	mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/api

# build-tools 生成数据库初始化与幂等续跑工具。
build-tools:
	mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP)-migrate ./cmd/migrate

# package 打包二进制、配置模板和运维资产。
package: build build-tools
	mkdir -p dist
	COPYFILE_DISABLE=1 tar -czf $(PACKAGE) $(PACKAGE_FILES)

# secret-scan 阻止本地配置和内联密钥进入版本控制。
secret-scan:
	@./scripts/check-secrets.sh

# promtool-check 优先使用本机工具，缺失时回退固定 Docker 镜像。
promtool-check:
	@if command -v promtool >/dev/null 2>&1; then promtool check rules $(PROMETHEUS_RULES); elif command -v docker >/dev/null 2>&1; then docker run --rm --entrypoint promtool -v "$$(pwd)/docs/prometheus:/rules:ro" $(PROMTOOL_IMAGE) check rules $(PROMETHEUS_RULES_IN_CONTAINER); else echo "promtool and docker not found, skip"; fi

# govulncheck 使用固定版本扫描实际可达漏洞。
govulncheck:
	GOTOOLCHAIN=$(GO_TOOLCHAIN) go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# security-scan 汇总密钥和依赖漏洞检查。
security-scan: secret-scan govulncheck

# diff-check 检查空白错误和冲突标记。
diff-check:
	git diff --check

BRANCH_BASE ?= main
BRANCH_VARIANT ?=

# branch-drift-check 限制长期分表方案分支的允许差异。
branch-drift-check:
	./scripts/check-table-sharding-drift.sh "$(BRANCH_BASE)" "$(BRANCH_VARIANT)"

# check 汇总提交前默认质量门禁。
check: fmt-check test vet build build-tools secret-scan promtool-check govulncheck diff-check

# ci 在默认门禁后追加竞态测试。
ci: branch-drift-check check test-race

# integration-env-up 启动并等待本地 MySQL 集成依赖。
integration-env-up:
	$(DOCKER_COMPOSE) -f deploy/integration/docker-compose.yml up -d --wait --wait-timeout $(INTEGRATION_WAIT_TIMEOUT)

# integration-env-down 删除本地集成容器和临时数据卷。
integration-env-down:
	$(DOCKER_COMPOSE) -f deploy/integration/docker-compose.yml down -v

# integration-test 负责启动依赖并执行真实数据库用例。
integration-test: integration-env-up integration-test-run

# integration-test-run 复用已启动的 MySQL 执行 integration 标签测试。
integration-test-run:
	INTEGRATION_MYSQL_DSN='$(INTEGRATION_MYSQL_DSN)' go test -count=1 -tags=integration ./internal/database
	INTEGRATION_MYSQL_DSN='$(INTEGRATION_MYSQL_DSN)' go test -count=1 -tags=integration ./internal/infra/mysql

MIGRATE_CONFIG ?= ./etc/config.yaml
MIGRATE_TIMEOUT ?= 15m

# migrate-status 只读登记状态，不执行初始化资产。
migrate-status:
	go run ./cmd/migrate -f $(MIGRATE_CONFIG) -action=status -timeout=$(MIGRATE_TIMEOUT)

# migrate-dry-run 校验初始化资产但不执行 DDL。
migrate-dry-run:
	go run ./cmd/migrate -f $(MIGRATE_CONFIG) -action=dry-run -timeout=$(MIGRATE_TIMEOUT)

# migrate-bootstrap 显式授权执行未登记资产，允许非空库和部分登记续跑。
migrate-bootstrap:
	go run ./cmd/migrate -f $(MIGRATE_CONFIG) -action=up -allow-bootstrap -timeout=$(MIGRATE_TIMEOUT)

# clean 删除本地构建和打包产物。
clean:
	rm -rf bin dist
