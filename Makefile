# Copyright (c) 2026 Ville Vesilehto
# SPDX-License-Identifier: MPL-2.0

SHELL := /bin/sh

# Benchmark knobs: ROUNDS interleaved rounds of COUNT samples each (after WARMUP
# discarded passes per process), so every benchmark ends up with ROUNDS*COUNT
# samples per implementation.
BENCH     ?= .
BENCHTIME ?= 0.5s
ROUNDS    ?= 3
COUNT     ?= 2
WARMUP    ?= 1
CPU       ?= 1
RESULTS   ?= benchmarks/results
UPSTREAM  ?= ../go-memdb
# The revision bench-self compares the working tree against.
BASE      ?= origin/main

.PHONY: all test race test-safe lint headers verify-upstream-tests generate-check \
	fuzz diff check bench-compare bench-aa bench-gate bench-summary bench-inpkg \
	bench-parallel bench-self bench-self-gate

all: check

## ---- correctness -----------------------------------------------------------

test:
	go test -count=1 ./...

race:
	go test -race -count=1 ./...

# Pure-safe build: no package unsafe on any hot path.
test-safe:
	go test -tags memdb_safe -count=1 ./...
	go test -tags purego -count=1 ./...

lint:
	@files=$$(gofmt -l . benchmarks); if [ -n "$$files" ]; then echo "gofmt needed:"; echo "$$files"; exit 1; fi
	go vet ./...
	cd benchmarks && go vet -composites=false ./... && go vet -composites=false -tags upstream ./...

headers:
	sh scripts/check-headers.sh

verify-upstream-tests:
	sh scripts/verify-upstream-tests.sh

generate-check:
	go generate ./...
	git diff --exit-code -- watch_few.go

fuzz:
	go test ./internal/radix -run '^$$' -fuzz FuzzTreeOps -fuzztime 60s
	go test ./internal/bitmap -run '^$$' -fuzz FuzzBitmap -fuzztime 60s
	cd benchmarks && go test ./differential -run '^$$' -fuzz FuzzOps -fuzztime 60s

# Differential tests against the original implementations.
diff:
	cd benchmarks && go test -count=1 ./differential ./radixdiff
	cd benchmarks && go build ./... && go build -tags upstream ./...

check: lint headers verify-upstream-tests test race test-safe diff

## ---- benchmarks ------------------------------------------------------------
#
# All A/B runs go through scripts/bench-compare.sh: one process per benchmark
# family, the two implementations interleaved per family, the order flipped
# every round. See BENCHMARKS.md.

BENCH_ENV = BENCH='$(BENCH)' BENCHTIME=$(BENCHTIME) ROUNDS=$(ROUNDS) COUNT=$(COUNT) WARMUP=$(WARMUP) CPU=$(CPU) RESULTS=results

# Interleaved A/B comparison against upstream, followed by the benchstat report.
bench-compare:
	$(BENCH_ENV) sh scripts/bench-compare.sh

# A/A run: upstream against itself. Its spread is the noise floor that
# calibrates the gate's tolerance.
bench-aa:
	$(BENCH_ENV) SIDES='upstream upstream' OUT_A=results/aa-1.txt OUT_B=results/aa-2.txt OUT_STAT=results/aa-benchstat.txt sh scripts/bench-compare.sh

# Regression check against our own history: the library of revision BASE and
# the working tree, under today's benchmark sources. bench-self-gate fails if
# any benchmark became slower or started allocating more.
bench-self:
	$(BENCH_ENV) BASE='$(BASE)' SIDES='base new' OUT_A=results/self-base.txt OUT_B=results/self-new.txt OUT_STAT=results/self-benchstat.txt sh scripts/bench-compare.sh

bench-self-gate:
	cd benchmarks && go run ./cmd/benchgate -old results/self-base.txt -new results/self-new.txt -old-name '$(BASE)' -allocs-slack 1

# Enforces the "universally faster" claim on the last bench-compare result.
bench-gate:
	cd benchmarks && go run ./cmd/benchgate -old results/upstream.txt -new results/new.txt

# The per-family summary table used in the README.
bench-summary:
	cd benchmarks && go run ./cmd/benchgate -old results/upstream.txt -new results/new.txt -summary

# Read scalability: the parallel benchmarks at 1, 4 and 8 procs.
bench-parallel:
	$(BENCH_ENV) BENCH='Parallel' CPU=1,4,8 OUT_A=results/parallel-upstream.txt OUT_B=results/parallel-new.txt OUT_STAT=results/parallel-benchstat.txt sh scripts/bench-compare.sh

# The three benchmarks that ship inside upstream's own test files, run in both
# checkouts (they exercise unexported code, so they cannot go through the shim).
bench-inpkg:
	@mkdir -p $(RESULTS)
	cd $(UPSTREAM) && go test -run '^$$' -bench . -benchmem -count 10 . | sed 's|^pkg: .*|pkg: memdb|' > $(CURDIR)/$(RESULTS)/inpkg-upstream.txt
	go test -run '^$$' -bench 'BenchmarkUUIDFieldIndex_parseString|BenchmarkCompoundMultiIndex_FromObject|BenchmarkWatch$$' -benchmem -count 10 . | sed 's|^pkg: .*|pkg: memdb|' > $(RESULTS)/inpkg-new.txt
	cd benchmarks && go tool benchstat ../$(RESULTS)/inpkg-upstream.txt ../$(RESULTS)/inpkg-new.txt | tee ../$(RESULTS)/benchstat-inpkg.txt
