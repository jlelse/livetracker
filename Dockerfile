FROM golang:1.24-alpine AS builder
WORKDIR /app
ENV GOFLAGS="-tags=linux,libsqlite3,sqlite_fts5"
RUN apk add --no-cache git gcc musl-dev
RUN apk add --no-cache --repository=https://dl-cdn.alpinelinux.org/alpine/edge/main sqlite-dev
COPY *.go go.mod go.sum .
COPY static /app/static
RUN go build -ldflags '-w -s' -o livetracker

FROM builder AS test
RUN go test -v ./...

FROM alpine:latest
WORKDIR /app
RUN apk add --no-cache --repository=https://dl-cdn.alpinelinux.org/alpine/edge/main sqlite-dev
COPY --from=builder /app/livetracker .
EXPOSE 8080
CMD ["./livetracker"]