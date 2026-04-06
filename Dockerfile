FROM golang:1.24-trixie AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -o /bin/fukan-ingest ./cmd/fukan-ingest

FROM debian:trixie-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*

COPY --from=builder /bin/fukan-ingest /usr/local/bin/fukan-ingest

ENTRYPOINT ["fukan-ingest"]
