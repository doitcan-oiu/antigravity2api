FROM node:22-alpine AS web
WORKDIR /app/web
RUN corepack enable && corepack prepare pnpm@10 --activate
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web .
RUN pnpm build

FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server .
COPY --from=web /app/server/web ./web
RUN CGO_ENABLED=0 go build -o /out/antigravity2api .

FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=build /out/antigravity2api /app/antigravity2api
ENV DATA_DIR=/app/data
ENV LISTEN_ADDR=:8080
EXPOSE 8080
VOLUME ["/app/data"]
ENTRYPOINT ["/app/antigravity2api"]
