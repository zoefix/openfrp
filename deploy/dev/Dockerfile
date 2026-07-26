# Builds both daemons into a scratch image.
#
# CGO_ENABLED=0 is what makes the scratch base viable: the binary has no libc
# dependency at all, which is the same property that lets one build run on
# every Linux distribution.
FROM golang:1.25 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/zoefix/openfrp/internal/version.Version=${VERSION}" \
    -o /out/ ./cmd/...

FROM scratch
COPY --from=build /out/openfrps /openfrps
COPY --from=build /out/openfrpc /openfrpc
