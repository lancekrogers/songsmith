# Build into bin/
build:
    go build -o {{bin_dir}}/{{binary_name}} ./cmd/songsmith

# Install into $GOBIN
install:
    go install ./cmd/songsmith

# Run songsmith with arguments
run *args:
    go run ./cmd/songsmith {{args}}

# Format all Go files
fmt:
    gofmt -w .
