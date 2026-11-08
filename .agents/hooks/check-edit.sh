#!/usr/bin/env bash
# 편집 직후 빠른 검사(PostToolUse). Claude Edit·Write·MultiEdit·NotebookEdit와 Codex apply_patch가 고친 파일만
# HERDR_CHECK_FILE_CMD로 검사하고, 실패하면 끝 코드 2와 stderr로 에이전트에게 돌려줘 그 자리에서 고치게 한다.
# 테스트는 돌리지 않는다(편집마다 돈다). 전체 검사는 [완료] 관문(herdr-report)이 맡는다.
# 검사 명령이 없거나 hook 자체가 실패하면 조용히 넘어간다. 편집을 막는 것은 검사 실패뿐이다.
case "${HERDR_ROLE:-}" in commander | orchestrator) exit 0 ;; esac
command -v jq >/dev/null || exit 0

input="$(cat)"
tool="$(printf '%s' "$input" | jq -r '.tool_name // ""' 2>/dev/null)"
cwd="$(printf '%s' "$input" | jq -r '.cwd // ""' 2>/dev/null)"
[[ -n "$cwd" ]] || cwd="$PWD"

files=()
case "$tool" in
  Edit | Write | MultiEdit | NotebookEdit)
    f="$(printf '%s' "$input" | jq -r '.tool_input.file_path // .tool_input.notebook_path // ""' 2>/dev/null)"
    [[ -n "$f" ]] && files+=("$f")
    ;;
  apply_patch)
    # patch 본문은 tool_input.command(문자열 또는 배열)에 있다. 추가·수정·이동 대상만 고른다.
    patch="$(printf '%s' "$input" | jq -r '.tool_input.command // .tool_input.patch // "" | if type == "array" then join("\n") else . end' 2>/dev/null)"
    while IFS= read -r f; do [[ -n "$f" ]] && files+=("$f"); done < <(
      printf '%s\n' "$patch" | sed -n -e 's/^\*\*\* Add File: //p' -e 's/^\*\*\* Update File: //p' -e 's/^\*\*\* Move to: //p'
    )
    ;;
  *) exit 0 ;;
esac
[[ ${#files[@]} -gt 0 ]] || exit 0

abs=()
for f in "${files[@]}"; do
  [[ "$f" == /* ]] || f="$cwd/$f"
  abs+=("$f")
done

out="$("$(dirname "${BASH_SOURCE[0]}")/../bin/herdr-check" --files "${abs[@]}" 2>&1)"
rc=$?
[[ $rc -eq 1 ]] || exit 0
printf '%s\n이 편집에서 생긴 문제면 지금 고친다.\n' "$out" >&2
exit 2
