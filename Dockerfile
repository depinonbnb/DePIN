FROM golang:1.21-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /out/server /app/server
RUN mkdir -p /data && chown -R app:app /data /app
USER app
ENV PORT=3000 DATABASE_URL=/data/depin.db
EXPOSE 3000
ENTRYPOINT ["/app/server"]
