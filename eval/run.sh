#!/usr/bin/env bash
# Replays every consecutive release of each repo through ripple and writes one CSV row per pair.
# usage: eval/run.sh RIPPLE_BIN OUT.csv REPO_DIR...
set -euo pipefail
bin=$1 out=$2; shift 2
echo "repo,from,to,declared,required,verdict,major_changes" > "$out"
for repo in "$@"; do
  name=$(basename "$repo")
  mapfile -t tags < <(git -C "$repo" tag --sort=v:refname | grep -E '^v?[0-9]+\.[0-9]+\.[0-9]+$')
  for ((i = 1; i < ${#tags[@]}; i++)); do
    a=${tags[i-1]} b=${tags[i]}
    json=$("$bin" diff --repo "$repo" --json "$a" "$b" 2>/dev/null) || [ $? -eq 2 ] || { echo "$name,$a,$b,,,error," >> "$out"; continue; }
    python -c 'import json,sys
r=json.load(sys.stdin); n=sys.argv[1:]
majors=";".join(c["address"] for c in r["changes"] if c["level"]=="major")
print(",".join(n+[r["declared"],r["required"],r["verdict"],majors]))' "$name" "$a" "$b" <<< "$json" >> "$out"
  done
done
