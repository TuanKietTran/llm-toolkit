.PHONY: build test run e2e smoke
build:
	go build -o gorouter ./cmd/gorouter
test:
	go test ./...
run: build
	./gorouter -config config.json
e2e:
	cd e2e && npm ci --silent && BROWSER="$${BROWSER:-/Applications/Brave Browser.app/Contents/MacOS/Brave Browser}" node dashboard.e2e.js
smoke:
	./scripts/smoke.sh
