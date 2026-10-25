FROM golang:1.26.2-bookworm AS base
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
FROM base AS test
RUN go test -race ./... && go vet ./...
FROM base AS build
RUN CGO_ENABLED=1 go build -trimpath -ldflags='-s -w' -o /atlas ./cmd/atlas
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /atlas /usr/local/bin/atlas
USER 10001:10001
ENV LISTEN_ADDR=:8080 DB_PATH=/data/atlas.db
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/atlas"]
