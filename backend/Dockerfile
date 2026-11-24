FROM golang:1.26 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /api ./cmd/api

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --no-create-home --shell /usr/sbin/nologin app

WORKDIR /app
COPY --from=build /api /api
COPY migrations ./migrations
COPY seeds ./seeds
COPY prompts ./prompts
COPY templates ./templates

USER app
EXPOSE 3000
ENTRYPOINT ["/api"]
