# The test receiver (scripts/test-receiver), for test and LAN-only copies.
#   docker build -f scripts/test-receiver/Dockerfile -t pagerarchive-test-receiver .
FROM golang:1.27.4@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY scripts/test-receiver ./scripts/test-receiver
RUN CGO_ENABLED=0 go build -trimpath -o /test-receiver ./scripts/test-receiver

FROM gcr.io/distroless/static-debian13:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5
COPY --from=build /test-receiver /test-receiver
EXPOSE 8097
ENTRYPOINT ["/test-receiver"]
