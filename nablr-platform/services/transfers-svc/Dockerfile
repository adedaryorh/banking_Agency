FROM golang:1.26.6-alpine AS builder
WORKDIR /workspace
RUN apk add --no-cache git
COPY services/transfers-svc/go.mod ./
RUN go mod download
COPY services/transfers-svc/. .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/transfers-svc ./cmd
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/transfers-worker ./cmd/worker

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /out/transfers-svc /usr/local/bin/transfers-svc
COPY --from=builder /out/transfers-worker /usr/local/bin/transfers-worker
EXPOSE 8002 9002
ENTRYPOINT ["transfers-svc"]

# Development stage with air for live reload
FROM golang:1.26.6-alpine AS dev
WORKDIR /workspace/services/transfers-svc
RUN apk add --no-cache git
RUN go install github.com/air-verse/air@latest
COPY services/transfers-svc/go.mod ./
RUN go mod download
COPY services/transfers-svc/. .
EXPOSE 8002 9002
CMD ["air"]
