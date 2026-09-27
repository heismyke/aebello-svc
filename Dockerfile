# Production image. Build: docker build -t aebello-svc .
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /api ./cmd/api

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H app
WORKDIR /app
COPY --from=build /api /app/api
COPY pricing.json /app/pricing.json
USER app
EXPOSE 8080
ENTRYPOINT ["/app/api"]
