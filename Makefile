build:
	go build
linux:
	env GOOS=linux GOARCH=amd64 go build
dev:
	env GOOS=linux GOARCH=amd64 go build && ./bin/testdev
test:
	go test ./...

coverage:
	go test -coverprofile test.cov
report: coverage
	go tool cover -html=test.cov

LDFLAGS := -X main.Version=$(VERSION)
shipit:
	@echo "Checking that VERSION was defined in the calling environment"
	@test -n "$(VERSION)"
	@echo "OK.  VERSION=$(VERSION)"

	@echo "Compiling Blacksmith Broker binaries..."
	rm -rf artifacts
	mkdir artifacts
	GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o artifacts/blacksmith-darwin-amd64 .
	GOOS=linux  GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o artifacts/blacksmith-linux-amd64  .

	@echo "Assembling Linux Server Distribution..."
	rm -f artifacts/*.tar.gz
	rm -rf blacksmith-$(VERSION)
	mkdir -p blacksmith-$(VERSION)
	cp artifacts/blacksmith-linux-amd64 blacksmith-$(VERSION)
	cp -a ui blacksmith-$(VERSION)
	tar -cjf artifacts/blacksmith-$(VERSION).tar.bz2 blacksmith-$(VERSION)/
	rm -rf blacksmith-$(VERSION)

.PHONY: build test coverage report shipit
