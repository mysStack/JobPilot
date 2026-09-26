FROM golang:1.24 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/jobpilot-controller ./cmd/controller

FROM docker-pull.ygxz.in/library/alpine:3.21

COPY --from=build /out/jobpilot-controller /jobpilot-controller
USER 65532:65532
ENTRYPOINT ["/jobpilot-controller"]
