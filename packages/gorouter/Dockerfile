# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gorouter ./cmd/gorouter
# distroless has no shell, so create the nonroot-owned data dir here and copy it across.
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gorouter /gorouter
COPY --from=build --chown=65532:65532 /data /data
# Seed config is read only on first start; afterwards the SQLite copy in /data wins.
# /data must exist in the image owned by nonroot, or a fresh named volume mounts root-owned
# and SQLite cannot open its database.
VOLUME ["/data"]
EXPOSE 20128
ENTRYPOINT ["/gorouter", "-config", "/config/config.json", "-db", "/data/gorouter.db"]
