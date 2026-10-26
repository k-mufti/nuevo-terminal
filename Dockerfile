# Build the web app
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Build the server (pure Go, no cgo needed for SQLite)
FROM golang:1.26-alpine AS server
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /nuevo ./cmd/nuevo

# Small runtime image
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app && mkdir /data && chown app /data
COPY --from=server /nuevo /app/nuevo
COPY --from=web /web/dist /app/web
COPY server/feeds.json /app/feeds.json
USER app
VOLUME /data
EXPOSE 8080
# Set PUBLIC_URL (e.g. https://news.example.com) to enable WebSub push.
ENV PUBLIC_URL=""
ENTRYPOINT ["/bin/sh", "-c", "exec /app/nuevo -addr :8080 -feeds /app/feeds.json -db /data/nuevo.db -web /app/web -public-url \"$PUBLIC_URL\""]
