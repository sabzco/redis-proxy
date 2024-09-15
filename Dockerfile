# syntax=docker/dockerfile:1

# Stage 1: Build the Go binary
FROM docker.sabz.dev/golang:1.22 AS builder

# Set the working directory
WORKDIR /app

# Copy go.mod and go.sum to the workspace
COPY go.mod go.sum ./

# Download dependencies
COPY go-redis go-redis
ARG HTTP_PROXY=10.0.0.0:8080
RUN go mod download

# Copy the source code to the workspace
COPY . .

# Build the Go binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o redis-proxy .

# Stage 2: Create a minimal image with the Go binary
FROM docker.sabz.dev/debian:latest

# Install ca-certificates for secure connections
# RUN apk --no-cache add ca-certificates

# Set the working directory
WORKDIR /usr/local/bin/

# Copy the binary from the builder stage
COPY --from=builder /app/redis-proxy .

# Expose the port that the application listens on
EXPOSE 6379
EXPOSE 8080

# Command to run the binary
CMD ["/usr/local/bin/redis-proxy"]
