FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /ripple ./cmd/ripple

# The terraform image is Alpine with git, both of which the runner needs.
FROM hashicorp/terraform:1.14.0
COPY --from=build /ripple /usr/local/bin/ripple
RUN adduser -D -u 10001 ripple
USER 10001
ENV HOME=/work TF_PLUGIN_CACHE_DIR=/work/.plugin-cache
ENTRYPOINT ["ripple"]
