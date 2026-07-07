FROM golang:1.26 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/tgbot .

FROM alpine:3.23.5
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /out/tgbot /usr/local/bin/tgbot
ENTRYPOINT ["tgbot"]
