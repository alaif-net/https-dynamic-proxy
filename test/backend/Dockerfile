FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY main.go ./
RUN go mod init test-backend && CGO_ENABLED=0 go build -o backend .

FROM scratch
COPY --from=builder /app/backend /backend
EXPOSE 443
ENTRYPOINT ["/backend"]
