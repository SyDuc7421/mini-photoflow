# One image recipe for every service. Build context is the repo root:
#   docker build -f microservices/Dockerfile --build-arg SVC=auth .
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG SVC
RUN CGO_ENABLED=0 go build -o /service ./microservices/${SVC}

FROM alpine:3.20
COPY --from=build /service /service
ENTRYPOINT ["/service"]
