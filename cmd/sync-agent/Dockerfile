FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/sync-agent ./cmd/sync-agent

FROM alpine:3.20
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /bin/sync-agent .
EXPOSE 7070/tcp 7070/udp 7071 7072
ENTRYPOINT ["./sync-agent"]
