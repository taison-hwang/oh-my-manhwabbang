#!/usr/bin/env bash
# 워커의 보고 누락을 막는다. 워커 화면은 아무도 읽지 않으므로 보고 없이 턴이 끝나면 자율 진행이 조용히 멈춘다.
#   worker-turn.sh start   UserPromptSubmit: 턴 시작 시각을 기록한다. 입력이 [배정]·[병합]·[검증]이면
#                          받은 시각과 작업 ID를 received.tsv에 남긴다(흐름 측정 herdr-flow용)
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
    prompt="$(printf '%s' "$input" | jq -r '.prompt // ""' | head -c 2000)"
    if [[ "$prompt" =~ \[(배정|병합|검증)\][[:space:]]+([^[:space:]]+) ]]; then
      tag="${BASH_REMATCH[1]}"
      task="${BASH_REMATCH[2]}"
      task="${task%%[,.:;—)(]*}"
      tsv="$dir/received.tsv"
      [ -s "$tsv" ] || printf 'epoch\ttime\trole\tsession_id\ttag\ttask\n' >"$tsv"
      printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$(date +%s)" "$(date +%Y-%m-%dT%H:%M:%S%z)" "$HERDR_ROLE" \
        "$(cat "$dir/.$HERDR_ROLE.session" 2>/dev/null)" "$tag" "$task" >>"$tsv"
    fi
    ;;
  stop)
    [ -f "$start_file" ] || exit 0
    started="$(cat "$start_file")"
    reported="$(cat "$reported_file" 2>/dev/null)"
    [ "${reported:-0}" -ge "${started:-0}" ] && exit 0
    if [ "$(printf '%s' "$input" | jq -r '.stop_hook_active // false')" = true ]; then
      # Orchestrator가 둘이면(교체 중) 가장 최근에 띄운 세션에 보낸다
      target="$(hc ls --json --label "project=$HERDR_PROJECT" --label role=orchestrator | jq -r '.terminals[].address' |
        while read -r t; do printf '%s %s\n' "$(hc get "$t" --json | jq -r '.created_ms // 0')" "$t"; done | sort -n | tail -n 1 | cut -d' ' -f2)"
      [ -n "$target" ] && hc send "$target" "[미보고] $HERDR_ROLE ${HC_TERMINAL_ID:-}: 보고 없이 턴을 끝냈다. hc read로 화면을 확인한다." >/dev/null
      date +%s >"$reported_file"
      exit 0
    fi
    jq -nc --arg r '보고 없이 턴을 끝낼 수 없다. 화면에 쓴 글은 아무도 읽지 않는다. 작업을 끝냈으면 herdr-report "[완료] <작업 ID> <변경 파일, 커밋, 검증 결과, 위험>", 병합을 끝냈으면 "[병합완료] <작업 ID> <병합한 SHA>", 질문에 답하는 턴이면 "[답변] <내용>"을 보낸다. 끝나지 않았으면 [진행](기다릴 대상과 다시 움직일 조건) 또는 [차단](필요한 결정)을 보낸다. 다른 말머리로 대신하거나 hc send로 직접 보내지 않는다.' \
      '{decision: "block", reason: $r}'
    ;;
esac
exit 0
