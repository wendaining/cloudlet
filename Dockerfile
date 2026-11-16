# syntax=docker/dockerfile:1
FROM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg
COPY migrations ./migrations
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/wendaining/cloudlet/internal/version.Version=${VERSION} -X github.com/wendaining/cloudlet/internal/version.Commit=${COMMIT}" -o /out/cloudlet-server ./cmd/server \
    && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/wendaining/cloudlet/internal/version.Version=${VERSION} -X github.com/wendaining/cloudlet/internal/version.Commit=${COMMIT}" -o /out/cloudlet ./cmd/cloudlet

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
LABEL org.opencontainers.image.source="https://github.com/wendaining/cloudlet"
LABEL org.opencontainers.image.licenses="Apache-2.0"
WORKDIR /app
COPY --from=build /out/cloudlet-server /app/cloudlet-server
COPY --from=build /out/cloudlet /app/cloudlet
COPY configs/container.json /app/config.json
EXPOSE 8080 8081
HEALTHCHECK --interval=10s --timeout=3s --start-period=3s --retries=3 CMD ["/app/cloudlet", "health", "--endpoint", "http://127.0.0.1:8080"]
ENTRYPOINT ["/app/cloudlet-server"]
CMD ["-config", "/app/config.json"]
