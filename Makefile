GO ?= go
GOFMT ?= gofmt

.PHONY: all build check test race lint vet format-check fuzz integration benchmark fixtures fixtures-check transcripts

all: check

build:
	@packages="$$(CGO_ENABLED=0 $(GO) list ./...)"; \
	if [ -n "$$packages" ]; then CGO_ENABLED=0 $(GO) build $$packages; else echo 'no Go packages to build'; fi

check: format-check build vet test

test:
	@packages="$$(CGO_ENABLED=0 $(GO) list ./...)"; \
	if [ -n "$$packages" ]; then CGO_ENABLED=0 $(GO) test $$packages; else echo 'no Go packages to test'; fi

race:
	@packages="$$(CGO_ENABLED=1 $(GO) list ./...)"; \
	if [ -n "$$packages" ]; then CGO_ENABLED=1 $(GO) test -race $$packages; else echo 'no Go packages to test'; fi

vet:
	@packages="$$(CGO_ENABLED=0 $(GO) list ./...)"; \
	if [ -n "$$packages" ]; then CGO_ENABLED=0 $(GO) vet $$packages; else echo 'no Go packages to vet'; fi

format-check:
	@files="$$(find . -type f -name '*.go' -not -path './.git/*' -print)"; \
	if [ -n "$$files" ]; then \
		unformatted="$$(printf '%s\n' "$$files" | xargs $(GOFMT) -l)"; \
		if [ -n "$$unformatted" ]; then printf 'unformatted files:\n%s\n' "$$unformatted"; exit 1; fi; \
	fi

lint: vet
	@command -v staticcheck >/dev/null 2>&1 || { echo 'staticcheck is required for make lint'; exit 1; }
	CGO_ENABLED=0 staticcheck ./...
	@command -v govulncheck >/dev/null 2>&1 || { echo 'govulncheck is required for make lint'; exit 1; }
	CGO_ENABLED=0 govulncheck ./...

fuzz:
	@while read package target; do \
		echo "fuzz $$package/$$target"; \
		CGO_ENABLED=0 $(GO) test "$$package" -run '^$$' -fuzz="^$$target$$" -fuzztime="$${FUZZTIME:-10s}" || exit 1; \
	done < scripts/fuzz-targets.txt

benchmark:
	CGO_ENABLED=0 $(GO) test -run '^$$' -bench . -benchmem ./delta ./fs/fsfs ./rasvn ./wc

integration:
	CGO_ENABLED=0 $(GO) test -tags integration ./...

fixtures:
	./scripts/fixtures/generate.sh

fixtures-check:
	./scripts/fixtures/check.sh

transcripts: fixtures
