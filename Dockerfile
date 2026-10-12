# syntax=docker/dockerfile:1
FROM golang:1.27.2 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# ksm-provider-api is private: modules are fetched with a .netrc passed as a BuildKit secret,
# so the token never ends up in an image layer:
#   docker build --secret id=netrc,src=$HOME/.netrc .
ENV GOPRIVATE=github.com/brongineer/*,github.com/BROngineer/*

COPY go.mod go.sum ./
RUN --mount=type=secret,id=netrc,target=/root/.netrc \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd/ cmd/
COPY internal/ internal/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o manager ./cmd

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]
