FROM golang:1.27-alpine AS build
WORKDIR /src
# one dependency, so its download is worth its own layer ahead of the source
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /icebreaker ./cmd/icebreaker

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /icebreaker /icebreaker
EXPOSE 8001
USER nonroot:nonroot
ENTRYPOINT ["/icebreaker"]
