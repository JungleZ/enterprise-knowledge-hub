# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS builder
WORKDIR /src

ENV GOPROXY=https://goproxy.cn,direct \
    CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o /out/server ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/server /app/server
EXPOSE 8080
ENTRYPOINT ["/app/server"]
