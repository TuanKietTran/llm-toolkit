# Job CV Finder core (Go, pure-Go SQLite, no CGO). Build context: packages/job-cv-finder
FROM golang:1.27.4-alpine3.24 AS build
WORKDIR /src/core
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY core/go.mod core/go.sum ./
RUN go mod download
COPY core/ ./
ARG VERSION=dev
RUN go build -trimpath -ldflags "-s -w" -o /out/jcf ./cmd/jcf

FROM alpine:3.24.4
RUN apk add --no-cache ca-certificates tzdata sqlite \
 && addgroup -S -g 10001 jcf && adduser -S -u 10001 -G jcf jcf \
 && mkdir -p /var/lib/jcf && chown jcf:jcf /var/lib/jcf
COPY --from=build /out/jcf /usr/local/bin/jcf
USER 10001:10001
ENV JCF_ADDR=0.0.0.0:8790 JCF_DB=/var/lib/jcf/jcf.db
EXPOSE 8790
HEALTHCHECK --interval=15s --timeout=3s --start-period=20s --retries=4 \
  CMD wget -qO- http://127.0.0.1:8790/readyz >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/jcf"]
