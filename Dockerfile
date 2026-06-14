# syntax=docker/dockerfile:1.6
#
# Single Dockerfile shared by every binary in this repository. The SERVICE
# build argument selects which entry-point under ./cmd to compile and bake
# into the final image.
#
# Build stage: compiles a fully static binary (CGO disabled because
# modernc.org/sqlite is pure Go) so the runtime stage does not need libc.
FROM golang:1.22-alpine AS build
WORKDIR /src

# Copy the module manifest first so the dependency-download layer stays
# warm across edits to the source tree. go.sum is generated on the fly by
# the subsequent `go mod tidy` if it is missing locally.
COPY go.mod ./
COPY go.sum* ./

COPY . .

ARG SERVICE
RUN CGO_ENABLED=0 GOOS=linux go mod tidy && \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w" \
      -o /out/app \
      ./cmd/${SERVICE}

# Runtime stage: alpine is preferred over distroless here because it makes
# it trivial to chown the data directory the SQLite/JSON stores write into.
# The size cost (~12 MB) is negligible for a school project and avoids the
# permission gymnastics distroless requires for writable volumes.
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S service && adduser -S service -G service && \
    mkdir -p /data && chown -R service:service /data && \
    mkdir -p /certs && chown -R service:service /certs

COPY --from=build --chown=service:service /out/app /app
COPY --chown=service:service certs/ /certs/

USER service
WORKDIR /data

ENTRYPOINT ["/app"]
