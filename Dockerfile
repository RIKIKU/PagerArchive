# Server image (API, browser app and validate-profile). amd64 only (SPEC.md section 8).
FROM node:24-slim@sha256:dc26ae92c9fb15cf6b1d63e20f0944b2efe0f115b20e00be48fb23fceb061c54 AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-fund --no-audit
COPY web/ ./
RUN npm run build

FROM golang:1.27.4@sha256:5e8afd493a7124a8586e134d9b532731161d4b9becf9dee5a5956b107426f93d AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/build ./web/build
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/pagerarchive ./cmd/pagerarchive

FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
COPY --from=build /out/pagerarchive /usr/local/bin/pagerarchive
# The made-up example, used when a deployment mounts no profile or map (R84).
COPY --from=build /src/profiles/example /usr/share/pagerarchive/example-profile
COPY --from=build /src/deploy/example-map/example-map.pmtiles /usr/share/pagerarchive/example-map.pmtiles
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/pagerarchive"]
CMD ["serve"]
