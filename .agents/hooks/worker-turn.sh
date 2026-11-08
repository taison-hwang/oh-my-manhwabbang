#!/usr/bin/env bash
# 워커의 보고 누락을 막는다. 워커 화면은 아무도 읽지 않으므로 보고 없이 턴이 끝나면 자율 진행이 조용히 멈춘다.
#   worker-turn.sh start   UserPromptSubmit: 턴 시작 시각을 기록한다
#   worker-turn.sh stop    Stop: 이 턴에 herdr-report를 보내지 않았으면 한 번 막는다.
#                          그래도 보내지 않으면 Orchestrator에 [미보고]를 직접 보내고 끝낸다.
case "${HERDR_ROLE:-}" in '' | commander | orchestrator) exit 0 ;; esac
[ -n "${HERDR_PROJECT:-}" ] || exit 0
exec 2>/dev/null

input="$(cat)"
dir="$HOME/.claude/herdr/handoff/$HERDR_PROJECT"
start_file="$dir/.$HERDR_ROLE.turn-start"
reported_file="$dir/.$HERDR_ROLE.reported"

case "${1:-}" in
  start)
    mkdir -p "$dir" && date +%s >"$start_file"
    ;;
  stop)
    [ -f "$start_file" ] || exit 0
    started="$(cat "$start_file")"
    reported="$(cat "$reported_file" 2>/dev/null)"
    [ "${reported:-0}" -ge "${started:-0}" ] && exit 0
    if [ "$(printf '%s' "$input" | jq -r '.stop_hook_active // false')" = true ]; then
      target="$(hc ls --json --label "project=$HERDR_PROJECT" --label role=orchestrator | jq -r '.terminals[0].address // empty')"
      [ -n "$target" ] && hc send "$target" "[미보고] $HERDR_ROLE ${HC_TERMINAL_ID:-}: 보고 없이 턴을 끝냈다. hc read로 화면을 확인한다." >/dev/null
      date +%s >"$reported_file"
      exit 0
    fi
    jq -nc --arg r '보고 없이 턴을 끝낼 수 없다. 화면에 쓴 글은 아무도 읽지 않는다. herdr-report "[완료] <작업 ID> <변경 파일, 커밋, 검증 결과, 위험>" 를 실행한다. 끝나지 않았으면 [차단] 또는 [진행]으로 이유와 다시 움직일 조건을 보낸다.' \
      '{decision: "block", reason: $r}'
    ;;
esac
exit 0
