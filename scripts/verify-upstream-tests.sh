#!/bin/sh
# Copyright (c) 2026 Ville Vesilehto
# SPDX-License-Identifier: MPL-2.0
#
# Proves that the files taken verbatim from hashicorp/go-memdb @ 7d3fdd5 --
# most importantly the complete upstream test suite, which serves as the
# compatibility oracle -- are byte-identical to upstream.
set -eu

cd "$(dirname "$0")/.."

if command -v sha256sum >/dev/null 2>&1; then
	sha256sum -c scripts/upstream-verbatim.sha256
else
	shasum -a 256 -c scripts/upstream-verbatim.sha256
fi
