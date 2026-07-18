.PHONY: all web server client test dev-server clean

all: web server client

web:
	cd web && npm install && npm run build

server:
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/agent-server ./server/cmd/agent-server

client:
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/agent-client ./client/cmd/agent-client

# cross-compile the client for common PC targets
client-all:
	GOOS=linux  GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/agent-client-linux-amd64  ./client/cmd/agent-client
	GOOS=linux  GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/agent-client-linux-arm64  ./client/cmd/agent-client
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/agent-client-darwin-arm64 ./client/cmd/agent-client

test:
	go vet ./...
	go test ./...

# local dev: server on :8080 with relaxed cookies, web via `cd web && npm run dev`
# (register the first account in the browser; it becomes the admin)
dev-server:
	AGENTDOCK_COOKIE_SECURE=false go run ./server/cmd/agent-server

clean:
	rm -rf bin data
