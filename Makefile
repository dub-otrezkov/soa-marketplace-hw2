MAVEN_IMAGE := maven:3.9.11-eclipse-temurin-21
MAVEN_VOLUME := soa-marketplace-maven

.PHONY: generate build test up down logs e2e demo clean

# Copy the context instead of bind mounting: this also works with a remote Docker
# daemon and Colima installations that do not share the workspace directory.
define maven
	@set -eu; \
	container=$$(docker create -w /workspace -v $(MAVEN_VOLUME):/root/.m2 $(MAVEN_IMAGE) mvn --batch-mode --no-transfer-progress $(1)); \
	trap 'docker rm -f "$$container" >/dev/null 2>&1' EXIT; \
	docker cp pom.xml "$$container:/workspace/pom.xml"; \
	docker cp src "$$container:/workspace/src"; \
	docker start --attach "$$container"; \
	status=$$(docker inspect --format '{{.State.ExitCode}}' "$$container"); \
	mkdir -p target; \
	docker cp "$$container:/workspace/target/." target/; \
	exit "$$status"
endef

generate:
	$(call maven,generate-sources)

build:
	$(call maven,clean package)

test:
	$(call maven,clean verify)

up:
	docker compose up --build --detach --wait

down:
	docker compose down

logs:
	docker compose logs --follow api

e2e:
	python3 scripts/e2e.py

demo:
	bash scripts/demo.sh

clean:
	rm -rf target
