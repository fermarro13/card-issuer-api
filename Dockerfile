# syntax=docker/dockerfile:1
FROM golang:1.27.1-bookworm AS build
ENV GOTOOLCHAIN=local CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go version && go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go test ./... && go vet ./... && go build -trimpath -ldflags="-s -w" -o /out/card-issuer-api ./cmd/api

FROM postgres:17.11-bookworm AS db-init
WORKDIR /workspace
COPY database /workspace/database
USER postgres
ENTRYPOINT ["sh", "/workspace/database/Initialize-Compose.sh"]

FROM scratch AS app
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/card-issuer-api /card-issuer-api
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/card-issuer-api"]
