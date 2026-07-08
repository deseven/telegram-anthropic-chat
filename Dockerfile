FROM golang:1.26 AS builder
WORKDIR /src
COPY go.mod go.sum ./
# The local goldmark-tgmd fork (referenced by a go.mod replace directive) must
# be present before `go mod download` so the replacement path resolves.
COPY third_party/ ./third_party/
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/tgbot .

FROM alpine:3.23.5
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /out/tgbot /usr/local/bin/tgbot
ENTRYPOINT ["tgbot"]
