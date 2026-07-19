# ---- web build ----
FROM node:24-alpine AS web
# override for hosts that can't reach registry.npmjs.org,
# e.g. NPM_REGISTRY=https://registry.npmmirror.com
ARG NPM_REGISTRY=https://registry.npmjs.org
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --registry=$NPM_REGISTRY
COPY web/ ./
# vite outDir points at ../server/internal/webui/dist
RUN mkdir -p ../server/internal/webui && npm run build

# ---- go build ----
FROM golang:1.25-alpine AS build
# override for hosts that can't reach proxy.golang.org,
# e.g. GOPROXY=https://goproxy.cn,direct
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/server/internal/webui/dist ./server/internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/agent-server ./server/cmd/agent-server
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/agent-client ./client/cmd/agent-client

# ---- runtime ----
FROM alpine:3.21
RUN adduser -D -u 1000 agentdock && mkdir -p /data && chown agentdock /data
COPY --from=build /out/agent-server /usr/local/bin/agent-server
# the client binary is also shipped in the image for easy extraction:
#   docker cp <container>:/usr/local/bin/agent-client .
COPY --from=build /out/agent-client /usr/local/bin/agent-client
USER agentdock
ENV AGENTDOCK_DB=/data/agentdock.db
EXPOSE 8080
VOLUME /data
ENTRYPOINT ["agent-server"]
