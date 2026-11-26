# Server image (API, browser app and validate-profile). amd64 only (SPEC.md section 8).
FROM node:24-slim@sha256:dc26ae92c9fb15cf6b1d63e20f0944b2efe0f115b20e00be48fb23fceb061c54 AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-fund --no-audit
COPY web/ ./
RUN npm run build

FROM golang:1.27.4@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/build ./web/build
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/pagerarchive ./cmd/pagerarchive

FROM gcr.io/distroless/static-debian13:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5
COPY --from=build /out/pagerarchive /usr/local/bin/pagerarchive
# The made-up example, used when a deployment mounts no profile or map (R84).
COPY --from=build /src/profiles/example /usr/share/pagerarchive/example-profile
COPY --from=build /src/deploy/example-map/example-map.pmtiles /usr/share/pagerarchive/example-map.pmtiles
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/pagerarchive"]
CMD ["serve"]
