GO ?= go
GO_IMAGE := golang:1.26.10-alpine

.PHONY: generate build test up down logs e2e demo clean

# Local Go is preferred. The Docker fallback copies the source instead of using
# bind mounts, so it also works with remote daemons and restricted Colima shares.
define run_go
	@set -eu; \
	if command -v "$(GO)" >/dev/null 2>&1; then \
		mkdir -p internal/api; "$(GO)" $(1); \
	else \
		container=$$(docker create -w /workspace -v soa-marketplace-go-mod:/go/pkg/mod -v soa-marketplace-go-build:/root/.cache/go-build $(GO_IMAGE) sh -c 'mkdir -p internal/api && go $(1)'); \
		trap 'docker rm -f "$$container" >/dev/null 2>&1' EXIT; \
		for source in go.mod go.sum api internal cmd; do docker cp "$$source" "$$container:/workspace/$$source"; done; \
		docker start --attach "$$container"; \
		status=$$(docker inspect --format '{{.State.ExitCode}}' "$$container"); \
		if [ "$$status" -eq 0 ]; then \
			mkdir -p internal/api; \
			docker cp "$$container:/workspace/internal/api/generated.go" internal/api/generated.go; \
			if [ "$(2)" = "build" ]; then mkdir -p bin; docker cp "$$container:/workspace/bin/marketplace" bin/marketplace; fi; \
		fi; \
		exit "$$status"; \
	fi
endef

generate:
	$(call run_go,generate ./api)

build: generate
	$(call run_go,build -trimpath -o bin/marketplace ./cmd/marketplace,build)

test: generate
	$(call run_go,test ./...)

up:
	docker compose up --build --detach --wait

down:
	docker compose down

logs:
	docker compose logs --follow api

e2e:
	python3 scripts/e2e.py --db-checks

demo:
	bash scripts/demo.sh

clean:
	rm -rf bin internal/api/generated.go
