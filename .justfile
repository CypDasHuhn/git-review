set shell := ["nu", "-c"]

binary := "git-review"

default:
    just --list

build:
    mkdir bin
    go build -o bin/{{binary}} ./cmd/git-review

test:
    go test ./...

install: test
    mkdir ($env.HOME | path join ".local" "bin")
    go build -o ($env.HOME | path join ".local" "bin" "{{binary}}") ./cmd/git-review
