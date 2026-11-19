# syntax=docker/dockerfile:1
ARG GO_VERSION=1.26

# ---- dev: toolchain for tests, lint and vulnerability checks (needs gcc for -race) ----
FROM golang:${GO_VERSION}-bookworm AS dev
RUN go install golang.org/x/vuln/cmd/govulncheck@latest \
 && curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b /usr/local/bin
WORKDIR /src
CMD ["bash"]

# ---- build: static binaries ----
FROM golang:${GO_VERSION}-bookworm AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/ ./cmd/... ./examples/city/backend

# ---- gateway runtime ----
FROM gcr.io/distroless/static-debian12:nonroot AS gateway
COPY --from=build /out/gateway /gateway
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/gateway"]

# ---- stdio to HTTP bridge for desktop MCP clients (run with docker run -i) ----
FROM gcr.io/distroless/static-debian12:nonroot AS bridge
COPY --from=build /out/gwbridge /gwbridge
USER nonroot
ENTRYPOINT ["/gwbridge"]

# ---- example fake backend runtime ----
FROM gcr.io/distroless/static-debian12:nonroot AS city-backend
COPY --from=build /out/backend /backend
USER nonroot
EXPOSE 9090
ENTRYPOINT ["/backend"]
