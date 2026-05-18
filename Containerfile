FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26@sha256:313faae491b410a35402c05d35e7518ae99103d957308e940e1ae2cfa0aac29b AS builder
ARG TARGETOS TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w -X main.version=${VERSION} -X main.revision=${REVISION}" \
    -o manager ./cmd/main.go

FROM gcr.io/distroless/static:nonroot@sha256:e3f945647ffb95b5839c07038d64f9811adf17308b9121d8a2b87b6a22a80a39

LABEL org.opencontainers.image.source="https://github.com/cozystack/kilo-clustermesh-operator"
LABEL org.opencontainers.image.description="Kubernetes ClusterMesh operator for Kilo"
LABEL org.opencontainers.image.licenses="Apache-2.0"
LABEL org.opencontainers.image.title="kilo-clustermesh-operator"

COPY --from=builder /workspace/manager /manager

USER 65532:65532

ENTRYPOINT ["/manager"]
