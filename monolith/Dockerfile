# Build context is the repo root: docker build -f monolith/Dockerfile .
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /monolith ./monolith

FROM alpine:3.20
COPY --from=build /monolith /monolith
EXPOSE 8080
ENTRYPOINT ["/monolith"]
