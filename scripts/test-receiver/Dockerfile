# The test receiver (scripts/test-receiver), for test and LAN-only copies.
#   docker build -f scripts/test-receiver/Dockerfile -t pagerarchive-test-receiver .
FROM golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY scripts/test-receiver ./scripts/test-receiver
RUN CGO_ENABLED=0 go build -trimpath -o /test-receiver ./scripts/test-receiver

FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
COPY --from=build /test-receiver /test-receiver
EXPOSE 8097
ENTRYPOINT ["/test-receiver"]
