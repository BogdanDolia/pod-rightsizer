FROM golang:1.20-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /pod-rightsizer cmd/pod-rightsizer/main.go

FROM alpine:3.16
COPY --from=builder /pod-rightsizer /pod-rightsizer
RUN chmod +x /pod-rightsizer
ENTRYPOINT ["/pod-rightsizer"]
CMD ["--help"]