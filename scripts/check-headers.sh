#!/bin/sh
# Copyright (c) 2026 Ville Vesilehto
# SPDX-License-Identifier: MPL-2.0
#
# Verifies the licensing headers of every Go source file:
#   - every file carries the MPL-2.0 SPDX identifier in its first lines;
#   - files derived from hashicorp/go-memdb keep the upstream copyright line.
set -eu

cd "$(dirname "$0")/.."

# Files that originate from upstream (verbatim or modified). They must keep
# the upstream copyright notice untouched (MPL-2.0 section 3.4).
UPSTREAM_FILES="
schema.go filter.go changes.go memdb.go index.go txn.go watch.go watch_few.go
watch-gen/main.go
filter_test.go index_test.go integ_test.go isolation_test.go memdb_test.go
schema_test.go txn_test.go watch_test.go
"

fail=0

for f in $(find . -name '*.go' -not -path './.git/*' | sed 's|^\./||' | sort); do
	if ! head -n 8 "$f" | grep -q 'SPDX-License-Identifier: MPL-2.0'; then
		echo "missing SPDX header: $f"
		fail=1
	fi
	if ! head -n 8 "$f" | grep -q 'Copyright'; then
		echo "missing copyright line: $f"
		fail=1
	fi
done

for f in $UPSTREAM_FILES; do
	[ -f "$f" ] || continue
	if [ "$(head -n 1 "$f")" != "// Copyright IBM Corp. 2015, 2026" ]; then
		echo "upstream copyright line altered or missing: $f"
		fail=1
	fi
	if [ "$(sed -n 2p "$f")" != "// SPDX-License-Identifier: MPL-2.0" ]; then
		echo "upstream SPDX line altered or missing: $f"
		fail=1
	fi
done

if [ "$fail" -ne 0 ]; then
	echo "header check FAILED"
	exit 1
fi
echo "header check OK"
