#!/usr/bin/env bash
# compact 직전(Claude·Codex PreCompact): git 상태를 스냅샷으로 남기고, 요약 지시를 출력한다.
# Claude는 표준 출력을 요약 지시로 쓰고 Codex는 무시한다. compact를 막지 않도록 항상 0으로 끝낸다.
[ -n "${HERDR_ROLE:-}" ] && [ -n "${HERDR_PROJECT:-}" ] || exit 0
input="$(cat)"
cwd="$(printf '%s' "$input" | jq -r '.cwd // ""' 2>/dev/null)"
dir="$HOME/.claude/herdr/handoff/$HERDR_PROJECT"
mkdir -p "$dir" 2>/dev/null || exit 0
{
  echo "시각: $(date -Iseconds)"
  echo "위치: ${cwd:-$PWD}"
  if git -C "${cwd:-.}" rev-parse --git-dir >/dev/null 2>&1; then
    echo "브랜치: $(git -C "${cwd:-.}" branch --show-current)"
    echo "최근 커밋:"
    git -C "${cwd:-.}" log --oneline -5
    echo "미커밋 변경:"
    git -C "${cwd:-.}" status --short | head -n 25
  fi
} >"$dir/$HERDR_ROLE.snapshot.md" 2>/dev/null

cat <<'EOF'
요약에 반드시 남긴다: 배정받은 작업 ID와 수용 기준, 사용자·Orchestrator의 결정과 이유, 지금까지 실패한 시도와 원인, 해결되지 않은 오류, 바로 다음 행동.
요약에서 뺀다: 도구 출력 원문, 파일 전문, 이미 해결된 오류의 경위. 코드와 git에서 알 수 있는 내용은 경로만 남긴다.
EOF
exit 0
