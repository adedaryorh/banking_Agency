FROM golang:1.26.6-alpine AS builder
WORKDIR /workspace
RUN apk add --no-cache git
COPY services/identity-svc/go.mod ./
RUN go mod download
COPY services/identity-svc/. .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/identity-svc ./cmd

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /out/identity-svc /usr/local/bin/identity-svc
EXPOSE 8001
ENTRYPOINT ["identity-svc"]

# Development stage with air for live reload
FROM golang:1.26.6-alpine AS dev
WORKDIR /workspace/services/identity-svc
RUN apk add --no-cache git
RUN go install github.com/air-verse/air@latest
COPY services/identity-svc/go.mod ./
RUN go mod download
COPY services/identity-svc/. .
EXPOSE 8001
CMD ["air"]
