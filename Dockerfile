FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
COPY --from=web /src/internal/ui/dist/app ./internal/ui/dist/app
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/intake-errors ./cmd/intake-errors

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/intake-errors /intake-errors
EXPOSE 8080
ENTRYPOINT ["/intake-errors"]
