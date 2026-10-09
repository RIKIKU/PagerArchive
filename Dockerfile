# Server image (API and validate-profile). amd64 only (SPEC.md section 8).
FROM golang:1.27.2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/pagerarchive ./cmd/pagerarchive

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/pagerarchive /usr/local/bin/pagerarchive
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/pagerarchive"]
CMD ["serve"]
