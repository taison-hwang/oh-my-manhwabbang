#!/usr/bin/env bash
# 세션 시작·/clear·compact 직후(Claude·Codex SessionStart): 역할과 읽을 파일을 주입한다.
# 전역 지침이 다른 스킬을 가리켜도 모델의 기억이나 탐색에 맡기지 않고 프로젝트 규칙으로 시작하게 한다.
# compact 직후에는 작업 상태 파일과 스냅샷도 함께 주입한다. 이후 모든 턴이 다시 읽으므로 길이를 제한한다.
# JSON으로 낸다. Codex 0.166은 이 시점의 평문 출력을 컨텍스트에 넣지 않는다(실측 2026-11-06).
[ -n "${HERDR_ROLE:-}" ] && [ -n "${HERDR_PROJECT:-}" ] || exit 0
input="$(cat)"
source_name="$(printf '%s' "$input" | jq -r '.source // ""' 2>/dev/null)"
dir="$HOME/.claude/herdr/handoff/$HERDR_PROJECT"
mkdir -p "$dir" 2>/dev/null
compacts="$dir/.$HERDR_ROLE.compacts"
case "$source_name" in
  compact) echo $(($(cat "$compacts" 2>/dev/null || echo 0) + 1)) >"$compacts" ;;
  resume) ;;
  *) rm -f "$compacts" "$dir/.$HERDR_ROLE.ctx" ;;
esac

case "$HERDR_ROLE" in
  commander | orchestrator) role_file="$HERDR_ROLE" ;;
  *) role_file=worker ;;
esac
skill="${HERDR_HOME:-.}/.agents/skills/running-herdr-autopilot"
{
  echo "[herdr 역할] 이 세션의 역할은 $HERDR_ROLE, 프로젝트는 $HERDR_PROJECT 다."
  echo "먼저 $skill/SKILL.md 와 $skill/references/$role_file.md 를 끝까지 읽고 따른다. ~/.agents/skills/operating-herdr-sessions 는 이 프로젝트에 적용되지 않으므로 전역 지침이 가리켜도 읽지 않는다."
  if [ "$role_file" = worker ]; then
    echo '모든 턴은 herdr-report "[완료] <내용>" 로 끝낸다. 화면에 쓴 글은 아무도 읽지 않는다.'
  fi
  if [ "$source_name" = compact ]; then
    echo "[compact 복구] 아래로 위치를 확인하고 수용 기준을 다시 읽은 뒤 이어 간다. 기억나지 않는 결정은 추측하지 말고 파일과 커밋에서 확인한다. 규칙: managing-session-context 스킬."
    if [ -f "$dir/$HERDR_ROLE.md" ]; then
      echo "--- 작업 상태($dir/$HERDR_ROLE.md)"
      head -n 40 "$dir/$HERDR_ROLE.md"
    else
      echo "--- 작업 상태 파일 없음. Orchestrator는 상태판과 세션 장부를 읽고, 워커는 상태 파일을 지금 만든다."
    fi
    if [ -f "$dir/$HERDR_ROLE.snapshot.md" ]; then
      echo "--- compact 직전 스냅샷"
      head -n 40 "$dir/$HERDR_ROLE.snapshot.md"
    fi
  fi
} | jq -Rs '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: .}}'
exit 0
