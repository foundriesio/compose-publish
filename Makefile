linter:=$(shell which golangci-lint 2>/dev/null || echo $(HOME)/go/bin/golangci-lint)

.PHONY: build
build:
	@mkdir -p bin/
	go build -o bin/compose-publish main.go

build-alpine:
	@mkdir -p bin/
	docker run \
		--rm -it -v $(PWD):$(PWD) \
		-e HOME=/tmp \
		-w $(PWD) \
		-u $(shell id -u):$(shell id -g) \
		golang:alpine go build -o bin/compose-publish main.go

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
publish: build-alpine
	gsutil cp ./bin/compose-publish gs://subscriber_registry/compose-publish
	gsutil -m acl ch -u AllUsers:R gs://subscriber_registry/compose-publish
