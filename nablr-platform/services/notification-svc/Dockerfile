FROM golang:1.26.6-alpine AS builder
WORKDIR /workspace
RUN apk add --no-cache git
COPY services/notification-svc/go.mod services/notification-svc/go.sum ./services/notification-svc/
WORKDIR /workspace/services/notification-svc
RUN go mod download
COPY services/notification-svc ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/notification-svc ./cmd

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /out/notification-svc /usr/local/bin/notification-svc
EXPOSE 8004
ENTRYPOINT ["notification-svc"]

# Development stage with air for live reload
FROM golang:1.26.6-alpine AS dev
WORKDIR /workspace/services/notification-svc
RUN apk add --no-cache git
RUN go install github.com/air-verse/air@latest
COPY services/notification-svc/go.mod services/notification-svc/go.sum ./
RUN go mod download
COPY services/notification-svc ./
EXPOSE 8004
CMD ["air"]
