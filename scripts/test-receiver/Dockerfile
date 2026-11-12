# The test receiver (scripts/test-receiver), for test and LAN-only copies.
#   docker build -f scripts/test-receiver/Dockerfile -t pagerarchive-test-receiver .
FROM golang:1.27.4@sha256:5e8afd493a7124a8586e134d9b532731161d4b9becf9dee5a5956b107426f93d AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY scripts/test-receiver ./scripts/test-receiver
RUN CGO_ENABLED=0 go build -trimpath -o /test-receiver ./scripts/test-receiver

FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
COPY --from=build /test-receiver /test-receiver
EXPOSE 8097
ENTRYPOINT ["/test-receiver"]
