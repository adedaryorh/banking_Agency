FROM golang:1.26.6-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git

# Copy go mod files
COPY services/api-gateway/go.mod services/api-gateway/go.sum* ./

# Download dependencies
RUN go mod download

# Copy source code
COPY services/api-gateway/ ./

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o api-gateway ./cmd

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /root/

# Copy the binary from builder
COPY --from=builder /app/api-gateway .

EXPOSE 8000

CMD ["./api-gateway"]

# Development stage with air for live reload
FROM golang:1.26.6-alpine AS dev

WORKDIR /workspace/services/api-gateway

RUN apk add --no-cache git

RUN go install github.com/air-verse/air@latest

COPY services/api-gateway/go.mod services/api-gateway/go.sum* ./
RUN go mod download

COPY services/api-gateway/ ./

EXPOSE 8000

CMD ["air"]
