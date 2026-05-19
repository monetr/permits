.PHONY: build test vet fmt fmt-check lint tidy clean

build:
	go build ./...
	go build -o permits ./cmd/permits

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

lint: vet fmt-check

tidy:
	go mod tidy

clean:
	rm -f permits
	rm -rf dist licenses
