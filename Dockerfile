FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/chatgpt-share-page ./cmd/server

FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && addgroup -S app \
    && adduser -S -G app app \
    && mkdir -p /data \
    && chown app:app /data

WORKDIR /app
COPY --from=build /out/chatgpt-share-page /usr/local/bin/chatgpt-share-page

ENV PORT=8080 \
    DATA_DIR=/data \
    DATABASE_PATH=/data/app.db \
    PUBLIC_BASE_URL=http://localhost:8080 \
    APP_BASE_URL=http://localhost:8080

USER app
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/chatgpt-share-page"]
