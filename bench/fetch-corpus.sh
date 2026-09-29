#!/bin/sh
# Downloads the scoring corpus into bench/corpus (gitignored). Every file is
# pinned to a commit and checked against the sha256 in corpus.tsv.
set -eu
cd "$(dirname "$0")"
mkdir -p corpus
bad=0
grep -v '^#' corpus.tsv | while IFS="$(printf '\t')" read -r kind repo sha path want; do
  name="${kind}__$(printf '%s/%s' "$repo" "$path" | tr '/' '_')"
  if [ ! -s "corpus/$name" ]; then
    curl -fsSL --max-time 60 "https://raw.githubusercontent.com/$repo/$sha/$path" -o "corpus/$name" || { echo "fetch failed: $repo/$path" >&2; rm -f "corpus/$name"; continue; }
  fi
  got=$(shasum -a 256 "corpus/$name" | cut -d' ' -f1)
  if [ "$got" != "$want" ]; then echo "sha256 mismatch: $name" >&2; rm -f "corpus/$name"; fi
done
echo "corpus files: $(ls corpus | wc -l | tr -d ' ')"
