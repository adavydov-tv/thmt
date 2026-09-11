# --- фронтенд ---
FROM node:22-alpine AS front
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci || npm install
COPY web/ ./
RUN npm run build

# --- бэкенд ---
FROM golang:1.24-alpine AS back
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# --- рантайм ---
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
WORKDIR /app
COPY --from=back  /out/server      /app/server
COPY --from=front /src/web/dist    /app/web/dist
USER app
ENV STATIC_DIR=/app/web/dist SERVER_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/app/server"]
