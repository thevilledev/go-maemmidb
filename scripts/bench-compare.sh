#!/bin/sh
# Copyright (c) 2026 Ville Vesilehto
# SPDX-License-Identifier: MPL-2.0
#
# A/B benchmark run of hashicorp/go-memdb ("upstream") against go-maemmidb
# ("new"), from the single benchmark source in benchmarks/.
#
#   - Both test binaries are built once, up front.
#   - Every benchmark FAMILY (top-level Benchmark function) runs in a process of
#     its own, so a family shares its heap only with the databases it needs,
#     never with the history of the families before it.
#   - The two implementations are interleaved per family and the order flips
#     every round (A B, then B A, ...), so drift hits both sides equally.
#
# Environment: BENCH (family regexp, default .), BENCHTIME (0.5s), ROUNDS (3),
# COUNT (2), CPU (1), RESULTS (benchmarks/results), SIDES ("upstream new";
# "upstream upstream" gives the A/A noise floor), OUT_A / OUT_B (file names).
set -eu

cd "$(dirname "$0")/../benchmarks"

BENCH=${BENCH:-.}
BENCHTIME=${BENCHTIME:-0.5s}
ROUNDS=${ROUNDS:-3}
COUNT=${COUNT:-2}
CPU=${CPU:-1}
RESULTS=${RESULTS:-results}
SIDES=${SIDES:-upstream new}
set -- $SIDES
SIDE_A=$1
SIDE_B=$2
OUT_A=${OUT_A:-$RESULTS/upstream.txt}
OUT_B=${OUT_B:-$RESULTS/new.txt}

if pgrep -f 'bench-(upstream|new)\.test|benchmarks\.test' >/dev/null 2>&1; then
	echo "another benchmark process is running; its load would corrupt the results" >&2
	exit 1
fi

mkdir -p "$RESULTS/tmp"
go test -c -tags upstream -o "$RESULTS/tmp/bench-upstream.test" .
go test -c -o "$RESULTS/tmp/bench-new.test" .

families=$("$RESULTS/tmp/bench-new.test" -test.list "$BENCH" | grep '^Benchmark' || true)
if [ -z "$families" ]; then
	echo "no benchmark matches $BENCH" >&2
	exit 1
fi

: >"$OUT_A"
: >"$OUT_B"

run() { # side family outfile
	"$RESULTS/tmp/bench-$1.test" -test.run '^$' -test.bench "^$2\$" -test.benchtime "$BENCHTIME" \
		-test.count "$COUNT" -test.cpu "$CPU" -test.timeout 0 >>"$3"
}

round=1
while [ "$round" -le "$ROUNDS" ]; do
	for family in $families; do
		printf 'round %s/%s  %s\n' "$round" "$ROUNDS" "$family"
		if [ $((round % 2)) -eq 1 ]; then
			run "$SIDE_A" "$family" "$OUT_A"
			run "$SIDE_B" "$family" "$OUT_B"
		else
			run "$SIDE_B" "$family" "$OUT_B"
			run "$SIDE_A" "$family" "$OUT_A"
		fi
	done
	round=$((round + 1))
done

go tool benchstat "$OUT_A" "$OUT_B" | tee "$RESULTS/benchstat.txt"
