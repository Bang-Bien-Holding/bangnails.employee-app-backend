# The build stage
FROM golang:1.25.12-alpine AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o api ./cmd

# The run stage
FROM scratch
WORKDIR /app
# Copy CA certificates
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/api .
# scratch has no /etc/passwd; use the numeric uid/gid for "nobody"
USER 65534:65534
EXPOSE 8080
CMD ["./api"]
