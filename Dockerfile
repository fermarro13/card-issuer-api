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
# Use Microsoft's signed Debian repository for PowerShell; psql comes from PostgreSQL's image.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && curl -fsSL https://packages.microsoft.com/config/debian/12/packages-microsoft-prod.deb -o /tmp/packages-microsoft-prod.deb \
    && dpkg -i /tmp/packages-microsoft-prod.deb \
    && apt-get update \
    && apt-get install -y --no-install-recommends powershell \
    && rm -f /tmp/packages-microsoft-prod.deb \
    && rm -rf /var/lib/apt/lists/*
ENV POWERSHELL_TELEMETRY_OPTOUT=1 DOTNET_CLI_TELEMETRY_OPTOUT=1
WORKDIR /workspace
COPY database /workspace/database
USER postgres
ENTRYPOINT ["pwsh", "-NoLogo", "-NoProfile", "-File", "/workspace/database/Initialize-Compose.ps1"]

FROM scratch AS app
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/card-issuer-api /card-issuer-api
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/card-issuer-api"]
