FROM golang:1.20 AS builder

WORKDIR /app

# Copy go.mod and go.sum first to leverage Docker cache
COPY go.mod ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o pinger .

FROM alpine:latest

RUN apk --no-cache add ca-certificates
COPY --from=builder /app/pinger /usr/local/bin/

ENTRYPOINT ["pinger"]
