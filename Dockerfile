# Build stage.
#
# The Go version is pinned to the one in go.mod rather than to "latest", so that
# a rebuild months from now produces the binary this was verified with.
FROM golang:1.25-alpine AS build

WORKDIR /src

# Dependencies are copied on their own so that editing source does not re-download
# the module cache on every rebuild.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO is off so the binary runs on a base image with no libc, and the build is
# stripped because the symbol table is only useful where the source is too.
# Every service comes from one image; the compose file picks the command. The
# partner recommender and natural language agent mocks are built alongside,
# apart from the prototype binaries.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ai-app-server ./cmd/ai-app-server && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ai-automation-engine ./cmd/ai-automation-engine && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mock-partner-recommender ./mock/partner/recommender/cmd/recommender && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mock-partner-nl-agent ./mock/partner/nlagent/cmd/nl-agent

# Runtime stage.
#
# alpine rather than scratch: the service reaches CB-Tumblebug and the Messages
# API over TLS and needs the CA bundle, and a shell makes the container
# debuggable, which matters more than the few megabytes for a prototype.
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

# A non-root user owns the archive directories and the deployment stage. Each
# service only ever appends to its own, so nothing needs to write anywhere else
# in the image.
RUN adduser -D -u 10001 aiapp
WORKDIR /app
RUN mkdir -p /app/data/archive /app/data/engine-archive /app/data/partner-nl-archive /var/lib/aiapp/stage && \
    chown -R aiapp:aiapp /app /var/lib/aiapp

COPY --from=build /out/ai-app-server /usr/local/bin/ai-app-server
COPY --from=build /out/ai-automation-engine /usr/local/bin/ai-automation-engine
COPY --from=build /out/mock-partner-recommender /usr/local/bin/mock-partner-recommender
COPY --from=build /out/mock-partner-nl-agent /usr/local/bin/mock-partner-nl-agent

USER aiapp
EXPOSE 8090 8091 8092 8093

# No settings file is baked in. Every setting has a default, so the container
# starts and serves the catalog, the matcher, the tools and the archive with
# nothing supplied; the deployment path needs AIAPP_TUMBLEBUG_BASE_URL and says
# so at startup when it is missing. The default command is the AI-App service;
# run /usr/local/bin/ai-automation-engine for the automation engine. The
# mock-partner-* binaries are partner institution mocks, not part of the
# prototype.
CMD ["/usr/local/bin/ai-app-server"]
