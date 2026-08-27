# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS builder

WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/qodercn-gateway ./cmd/qodercn-gateway

FROM alpine:3.21

RUN apk add --no-cache ca-certificates \
    && addgroup -S qodercn \
    && adduser -S -G qodercn qodercn
COPY --from=builder /out/qodercn-gateway /usr/local/bin/qodercn-gateway

USER qodercn
EXPOSE 8095
ENTRYPOINT ["qodercn-gateway"]
CMD ["--host", "0.0.0.0", "--port", "8095"]
