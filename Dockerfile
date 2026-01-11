FROM golang:1.22 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /bot .
FROM gcr.io/distroless/static:nonroot
WORKDIR /app

COPY --from=build /bot /app/bot

USER nonroot:nonroot
ENTRYPOINT ["/app/bot"]
