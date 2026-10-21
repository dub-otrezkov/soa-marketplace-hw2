# syntax=docker/dockerfile:1
FROM maven:3.9.11-eclipse-temurin-21 AS build
WORKDIR /workspace
COPY pom.xml ./
COPY src ./src
RUN --mount=type=cache,target=/root/.m2 mvn --batch-mode --no-transfer-progress clean package

FROM eclipse-temurin:21-jre-alpine
WORKDIR /app
RUN addgroup -S marketplace && adduser -S -G marketplace marketplace
COPY --from=build --chown=marketplace:marketplace /workspace/target/marketplace-1.0.0.jar app.jar
USER marketplace
EXPOSE 8080
ENTRYPOINT ["java", "-jar", "/app/app.jar"]
