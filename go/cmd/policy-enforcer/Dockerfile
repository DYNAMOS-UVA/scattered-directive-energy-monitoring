FROM haskell:9.6.6 AS eflint-builder

# eFLINT v4.1.0.1 selects its compiler by the ghc-9.2 executable name.
RUN ln -s "$(command -v ghc)" /usr/local/bin/ghc-9.2

ARG EFLINT_REPO=https://gitlab.com/eflint/haskell-implementation.git
ARG EFLINT_TAG=v4.1.0.1

RUN git clone --depth 1 --branch "${EFLINT_TAG}" "${EFLINT_REPO}" /tmp/eflint-project
WORKDIR /tmp/eflint-project
RUN cabal update
RUN cabal install
RUN cp /root/.local/bin/eflint-server /usr/bin/eflint-server

FROM golang:1.22.1-bullseye AS go-builder
ARG NAME
ENV NAME=$NAME

WORKDIR /app
COPY go.mod go.sum ./
COPY pkg ./pkg
COPY cmd/$NAME ./cmd/$NAME

# The policy enforcer supplies its own module files for standalone development.
# Its container build uses the repository module so it can import /app/pkg.
RUN rm -f ./cmd/$NAME/go.mod ./cmd/$NAME/go.sum

WORKDIR /app/cmd/$NAME

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o /go/bin/$NAME .

FROM debian:bookworm-slim
ARG NAME
ENV NAME=$NAME

RUN apt-get update && apt-get install -y --no-install-recommends \
    libgmp10 \
    libc6 \
    libffi8 \
    libnuma1 \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app/

# Copy eflint-server from the eflint build stage
COPY --from=eflint-builder /usr/bin/eflint-server /usr/bin/eflint-server

# Copy the pre-built service binary
COPY --from=go-builder /go/bin/$NAME .

# Start the application
CMD ["/bin/sh", "-c", "/app/$NAME || ls -al /app"]
