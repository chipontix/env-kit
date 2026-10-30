FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /api ./cmd/api

FROM alpine:3.22
RUN adduser -D -u 10001 app
COPY --from=build /api /api
USER app
EXPOSE 8080
ENTRYPOINT ["/api"]
