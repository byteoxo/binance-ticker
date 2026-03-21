default: build

build:
    go build -o crypto-ticker .

run: build
    ./crypto-ticker

lint:
    go vet ./...

test:
    go test ./...

tidy:
    go mod tidy

check: tidy lint test

release-dry:
    goreleaser release --snapshot --clean

release tag:
    git tag {{tag}}
    git push origin {{tag}}

clean:
    rm -f crypto-ticker
    rm -rf dist/
