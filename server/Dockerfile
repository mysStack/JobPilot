FROM golang:1.24 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/jobpilot-controller ./cmd/controller

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/jobpilot-controller /jobpilot-controller
USER nonroot:nonroot
ENTRYPOINT ["/jobpilot-controller"]
