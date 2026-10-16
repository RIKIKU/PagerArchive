# The test receiver (scripts/test-receiver), for test and LAN-only copies.
#   docker build -f scripts/test-receiver/Dockerfile -t pagerarchive-test-receiver .
FROM golang:1.27.2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY scripts/test-receiver ./scripts/test-receiver
RUN CGO_ENABLED=0 go build -trimpath -o /test-receiver ./scripts/test-receiver

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /test-receiver /test-receiver
EXPOSE 8097
ENTRYPOINT ["/test-receiver"]
