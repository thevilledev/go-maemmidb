#!/bin/sh
# Copyright (c) 2026 Ville Vesilehto
# SPDX-License-Identifier: MPL-2.0
#
# Prints the Markdown "Results" section of BENCHMARKS.md from the files a
# `make bench-compare` run (and, if present, `make bench-inpkg` and
# `make bench-parallel`) left in benchmarks/results.
set -eu

cd "$(dirname "$0")/../benchmarks"
R=results

samples() { # file -> "min-max" samples per benchmark
	awk '/^Benchmark/ {n[$1]++} END {lo=0; hi=0; for (k in n) { if (lo==0 || n[k]<lo) lo=n[k]; if (n[k]>hi) hi=n[k] }; if (lo==hi) print lo; else print lo "-" hi}' "$1"
}

echo "### Environment"
echo
echo '```'
grep -m1 '^goos:' "$R/new.txt"
grep -m1 '^goarch:' "$R/new.txt"
grep -m1 '^cpu:' "$R/new.txt"
go version | sed 's/^/go:     /'
echo "upstream: $(grep 'hashicorp/go-memdb ' go.mod | awk '{print $(NF-1), $NF}'), $(grep 'go-immutable-radix' go.mod | awk '{print $1, $2}')"
echo "samples:  $(samples "$R/new.txt") per benchmark and implementation"
echo '```'
echo
echo "### The gate"
echo
echo '```'
go run ./cmd/benchgate -old "$R/upstream.txt" -new "$R/new.txt" 2>&1 || true
echo '```'
echo
echo "### Summary by benchmark family"
echo
echo "Speed-up is upstream's time divided by go-maemmidb's (median of the samples);"
echo "allocs/op and B/op are the change in the family's total."
echo
go run ./cmd/benchgate -old "$R/upstream.txt" -new "$R/new.txt" -summary
echo
if [ -f "$R/inpkg-upstream.txt" ] && [ -f "$R/inpkg-new.txt" ]; then
	echo "### Upstream's own benchmarks"
	echo
	echo "The three benchmarks in go-memdb's test files, run in both checkouts:"
	echo
	echo '```'
	go tool benchstat "$R/inpkg-upstream.txt" "$R/inpkg-new.txt" 2>/dev/null | grep -v '^ *$'
	echo '```'
	echo
fi
if [ -f "$R/parallel-upstream.txt" ] && [ -f "$R/parallel-new.txt" ]; then
	echo "### Parallel readers"
	echo
	echo '```'
	go tool benchstat "$R/parallel-upstream.txt" "$R/parallel-new.txt" 2>/dev/null | grep -v '^ *$' | awk '/sec\/op/ {p=1} /B\/op/ {p=0} p'
	echo '```'
	echo
fi
echo "### Full results"
echo
echo "The raw samples are in [\`benchmarks/results/upstream.txt\`](benchmarks/results/upstream.txt) and"
echo "[\`benchmarks/results/new.txt\`](benchmarks/results/new.txt); the complete \`benchstat\` comparison"
echo "(time, bytes, allocations and the heap metrics of every benchmark) is in"
echo "[\`benchmarks/results/benchstat.txt\`](benchmarks/results/benchstat.txt)."
