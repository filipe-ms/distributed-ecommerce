# syntax=docker/dockerfile:1.6
#
# Dockerfile único usado por todos os binários do repositório. O argumento
# de build SERVICE diz qual entrypoint dentro de ./cmd vai ser compilado.
#
# Stage de build: gera um binário estático (CGO desligado porque o
# modernc.org/sqlite é Go puro), então o stage de runtime não precisa
# de libc.
FROM golang:1.22-alpine AS build
WORKDIR /src

# Copia o go.mod antes do resto pra camada de download de dependências
# ficar quentinha entre builds. O go.sum é gerado pelo `go mod tidy`
# logo depois, se não existir.
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

# Stage de runtime: alpine em vez de distroless porque é trivial dar
# chown na pasta de dados onde o SQLite/JSON gravam. Os ~12 MB extras
# não fazem diferença num trabalho de faculdade e evitam ginástica de
# permissões.
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
