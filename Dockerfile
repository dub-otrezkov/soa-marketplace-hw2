# syntax=docker/dockerfile:1
# Flyway remains a separate migration tool; the application is entirely Go.
FROM maven:3.9.11-eclipse-temurin-21 AS migrate
WORKDIR /flyway
COPY db/flyway-pom.xml pom.xml
COPY db/migration ./sql
RUN mvn --batch-mode --no-transfer-progress -Dmaven.repo.local=/opt/maven-repository -Dflyway.skip=true flyway:migrate
ENTRYPOINT ["mvn", "--offline", "--batch-mode", "--no-transfer-progress", "-Dmaven.repo.local=/opt/maven-repository", "flyway:migrate"]

FROM golang:1.26.10-alpine AS build
WORKDIR /workspace
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY api ./api
COPY internal ./internal
COPY cmd ./cmd
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p internal/api && go generate ./api && go test ./... && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/marketplace ./cmd/marketplace

FROM alpine:3.22 AS app
RUN apk add --no-cache ca-certificates && addgroup -S marketplace && adduser -S -G marketplace marketplace
WORKDIR /app
COPY --from=build --chown=marketplace:marketplace /out/marketplace /app/marketplace
USER marketplace
EXPOSE 8080
ENTRYPOINT ["/app/marketplace"]
