# helm: .containers.main
FROM golang:1.22.3-alpine3.18 as builder

ARG GOPROXY

RUN apk add --no-cache git && rm -rf /var/cache/apk/*

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download
COPY . .

WORKDIR /app
RUN export LOCAL_DEV=NOT
RUN CGO_ENABLED=0 go build -o service

FROM golang:1.22.3-alpine3.18 AS final

WORKDIR /app
COPY --from=builder /app/service .

RUN addgroup -g 1000 app \
    && adduser -G app -u 1000 app -D

CMD ["./service"]