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
#   - Every round runs binaries with a different, randomised function layout
#     (the linker's -randlayout). Where the linker happens to put a hot loop or
#     a runtime function relative to cache-line and page boundaries is worth
#     tens of percent on small benchmarks on some CPUs: one build of this very
#     suite measured 9 ns and 13 ns for the same source on a Zen 5, depending
#     only on the layout. With one layout per side that luck is a bias nobody
#     can see; with one per round it is noise, which the statistics can.
#   - The first pass of every process is a warm-up and is thrown away. During
#     that pass the process is still building its databases: the heap grows by
#     hundreds of megabytes between two benchmarks and the collector runs back
#     to back, which has nothing to do with the code being measured and made
#     the first sample of a 100,000-row write benchmark up to four times
#     slower than every later one.
#
# Environment: BENCH (family regexp, default .), BENCHTIME (0.5s), ROUNDS (3),
# COUNT (2 samples per round, after WARMUP (1) discarded passes), CPU (1),
# RESULTS (benchmarks/results), SIDES ("upstream new"; "upstream upstream" gives
# the A/A noise floor; "base new" compares against our own history; a third
# side, as in "upstream base new", is interleaved with the other two and
# compared with the last one), BASE (a git revision: its library is built into
# the "base" side, from the CURRENT benchmark sources), OUT_A / OUT_B / OUT_C /
# OUT_STAT (file names).
set -eu

cd "$(dirname "$0")/../benchmarks"

BENCH=${BENCH:-.}
BENCHTIME=${BENCHTIME:-0.5s}
ROUNDS=${ROUNDS:-3}
COUNT=${COUNT:-2}
WARMUP=${WARMUP:-1}
CPU=${CPU:-1}
RESULTS=${RESULTS:-results}
SIDES=${SIDES:-upstream new}
BASE=${BASE:-}
set -- $SIDES
SIDE_A=$1
SIDE_B=$2
SIDE_C=${3:-}
if [ -n "$SIDE_C" ]; then
	# Three sides: A and B are both baselines for C.
	OUT_A=${OUT_A:-$RESULTS/$SIDE_A.txt}
	OUT_B=${OUT_B:-$RESULTS/$SIDE_B.txt}
	OUT_C=${OUT_C:-$RESULTS/$SIDE_C.txt}
else
	OUT_A=${OUT_A:-$RESULTS/upstream.txt}
	OUT_B=${OUT_B:-$RESULTS/new.txt}
fi
OUT_STAT=${OUT_STAT:-$RESULTS/benchstat.txt}

if pgrep -f 'bench-(upstream|new|base)(-r[0-9]+)?\.test|benchmarks\.test' >/dev/null 2>&1; then
	echo "another benchmark process is running; its load would corrupt the results" >&2
	exit 1
fi

mkdir -p "$RESULTS/tmp"
TMP=$(cd "$RESULTS/tmp" && pwd)
rm -f "$TMP"/bench-*.test

if [ -n "$BASE" ]; then
	# The library of revision BASE under the benchmark sources of today, so
	# that both sides run exactly the same benchmarks. Benchmarks of API that
	# BASE does not have yet are excluded by the "base" build tag.
	rm -rf "$TMP/base-src"
	mkdir -p "$TMP/base-src"
	git -C .. archive "$BASE" | tar -x -C "$TMP/base-src"
	rm -rf "$TMP/base-src/benchmarks"
	mkdir "$TMP/base-src/benchmarks"
	cp ./*.go go.mod go.sum "$TMP/base-src/benchmarks/"
fi

# One set of binaries per round, each linked with its own function layout.
round=1
while [ "$round" -le "$ROUNDS" ]; do
	layout="-ldflags=-randlayout=$round"
	go test -c "$layout" -tags upstream -o "$TMP/bench-upstream-r$round.test" .
	go test -c "$layout" -o "$TMP/bench-new-r$round.test" .
	if [ -n "$BASE" ]; then
		(cd "$TMP/base-src/benchmarks" && go test -c "$layout" -tags base -o "$TMP/bench-base-r$round.test" .)
	fi
	round=$((round + 1))
done
rm -rf "$TMP/base-src"

families=$("$TMP/bench-new-r1.test" -test.list "$BENCH" | grep '^Benchmark' || true)
if [ -z "$families" ]; then
	echo "no benchmark matches $BENCH" >&2
	exit 1
fi

: >"$OUT_A"
: >"$OUT_B"
[ -z "$SIDE_C" ] || : >"$OUT_C"

run() { # side family outfile
	"$TMP/bench-$1-r$round.test" -test.run '^$' -test.bench "^$2\$" -test.benchtime "$BENCHTIME" \
		-test.count "$((COUNT + WARMUP))" -test.cpu "$CPU" -test.timeout 0 >"$TMP/pass.txt"
	awk -v warm="$WARMUP" '/^Benchmark/ { if (++seen[$1] <= warm) next } { print }' "$TMP/pass.txt" >>"$3"
}

round=1
while [ "$round" -le "$ROUNDS" ]; do
	for family in $families; do
		printf 'round %s/%s  %s\n' "$round" "$ROUNDS" "$family"
		if [ -n "$SIDE_C" ]; then
			# Rotate, so that every side runs first, second and last.
			case $((round % 3)) in
			1) order="A B C" ;;
			2) order="B C A" ;;
			0) order="C A B" ;;
			esac
		elif [ $((round % 2)) -eq 1 ]; then
			order="A B"
		else
			order="B A"
		fi
		for side in $order; do
			case $side in
			A) run "$SIDE_A" "$family" "$OUT_A" ;;
			B) run "$SIDE_B" "$family" "$OUT_B" ;;
			C) run "$SIDE_C" "$family" "$OUT_C" ;;
			esac
		done
	done
	round=$((round + 1))
done
rm -f "$TMP/pass.txt"

if [ -n "$SIDE_C" ]; then
	go tool benchstat "$OUT_A" "$OUT_C" | tee "$OUT_STAT"
	go tool benchstat "$OUT_B" "$OUT_C" >"${OUT_STAT%.txt}-$SIDE_B.txt"
else
	go tool benchstat "$OUT_A" "$OUT_B" | tee "$OUT_STAT"
fi
