linter:=$(shell which golangci-lint 2>/dev/null || echo $(HOME)/go/bin/golangci-lint)

.PHONY: build-static
build-static:
	@mkdir -p bin/
	CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o bin/compose-publish main.go

.PHONY: test
test:
	go test ./... -v

.PHONY: fmt
fmt:
	@goimports -e -w ./

.PHONY: check
check:
	@test -z $(shell gofmt -l ./ | tee /dev/stderr) || echo "[WARN] Fix formatting issues with 'make fmt'"
	@test -x $(linter) || (echo "Please install linter from https://github.com/golangci/golangci-lint/releases/tag/v1.25.1 to $(HOME)/go/bin")
	$(linter) run

.PHONY: publish
publish: build-static
	gsutil cp ./bin/compose-publish gs://subscriber_registry/compose-publish
	gsutil -m acl ch -u AllUsers:R gs://subscriber_registry/compose-publish

fioapp:
	@mkdir -p bin/
	go build -o bin/fioapp cmd/fioapp/main.go
