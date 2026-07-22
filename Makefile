.PHONY: build test test-race vet fmt fmt-check lint generate verify-generated integration architecture clean check

build:
	mkdir -p build
	CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o build/golem ./cmd/golem

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w $$(find cmd internal tests tools -type f -name '*.go')

fmt-check:
	@files="$$(gofmt -l $$(find cmd internal tests tools -type f -name '*.go'))"; \
	if [ -n "$$files" ]; then \
		echo "$$files"; \
		exit 1; \
	fi

lint: vet architecture verify-generated

generate:
	go run ./tools/registrygen -source testdata/registry -output internal/registry/data

verify-generated:
	go run ./tools/registrygen -check -source testdata/registry -output internal/registry/data

integration:
	go test ./tests/integration/...

architecture:
	go test ./internal/architecture

clean:
	rm -rf build

check: fmt-check test vet architecture verify-generated
