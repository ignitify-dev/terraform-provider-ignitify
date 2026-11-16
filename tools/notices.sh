#!/bin/sh
# Writes the third-party notices to $1: the licence of each Go module linked
# into the provider (go-licenses), then the NOTICE file of each linked module
# that has one (Apache-2.0 section 4(d)). Fails on a licence outside $2, a
# comma-separated list. Runs in the build container (see the Makefile).
set -eu

out=$1
allowed=$2
go_licenses=$3
own_module=github.com/ignitify-dev/terraform-provider-ignitify
rule='================================================================================'
dash=--------------------------------------------------------------------------------

"$go_licenses" check . --allowed_licenses="$allowed"
"$go_licenses" report . --ignore "$own_module" --template tools/notices.tpl > "$out"

# Listed first, so a failed `go list` stops the script (a pipe would hide it).
modules=$(go list -deps -f '{{with .Module}}{{.Path}}@{{.Version}} {{.Dir}}{{end}}' .)
printf '%s\n' "$modules" | sort -u |
while read -r module dir; do
	[ "${module%%@*}" = "$own_module" ] && continue
	for notice in "$dir"/NOTICE*; do
		[ -f "$notice" ] || continue
		printf '\n%s\nNOTICE of %s\n%s\n' "$rule" "$module" "$dash" >> "$out"
		cat "$notice" >> "$out"
	done
done
