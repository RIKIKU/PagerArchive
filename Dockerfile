# Server image (API, browser app and validate-profile). amd64 only (SPEC.md section 8).
FROM node:24-slim AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-fund --no-audit
COPY web/ ./
RUN npm run build

FROM golang:1.27.2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/build ./web/build
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/pagerarchive ./cmd/pagerarchive

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/pagerarchive /usr/local/bin/pagerarchive
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/pagerarchive"]
CMD ["serve"]
