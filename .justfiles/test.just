[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Unit tests
[no-cd]
unit:
    go test ./...

# Unit tests with the race detector
[no-cd]
race:
    go test -race ./...

# go vet and gofmt check
[no-cd]
lint:
    go vet ./...
    @test -z "$(gofmt -l .)" || (gofmt -l . && echo "gofmt needed" && exit 1)
