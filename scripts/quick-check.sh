#!/usr/bin/env bash
# 편집 직후 빠른 검사(하네스 HERDR_CHECK_FILE_CMD). 고친 파일만 본다. 테스트는 돌리지 않는다.
#   .go          gofmt -l 로 포맷 확인
#   web/ ts·tsx  eslint(web/node_modules가 없으면 건너뛴다)
# 사용: scripts/quick-check.sh <파일…>   (작업 트리 최상위에서 실행)
set -uo pipefail
go_files=() web_files=()
for f in "$@"; do
  [[ -f "$f" ]] || continue
  case "$f" in
    *.go) go_files+=("$f") ;;
    */web/*.ts | */web/*.tsx | web/*.ts | web/*.tsx) web_files+=("$(realpath "$f")") ;;
  esac
done

rc=0
if [[ ${#go_files[@]} -gt 0 ]]; then
  bad="$(gofmt -l "${go_files[@]}")"
  if [[ -n "$bad" ]]; then
    echo "gofmt가 필요하다(gofmt -w 로 고친다):"
    printf '  %s\n' $bad
    rc=1
  fi
fi
if [[ ${#web_files[@]} -gt 0 ]]; then
  if [[ -x web/node_modules/.bin/eslint ]]; then
    (cd web && ./node_modules/.bin/eslint --no-warn-ignored "${web_files[@]}") || rc=1
  else
    echo "web/node_modules가 없어 eslint를 건너뛴다" >&2
  fi
fi
exit $rc
