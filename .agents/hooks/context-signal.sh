#!/usr/bin/env bash
# 소프트 신호(Claude PostToolUse·UserPromptSubmit): 맥락이 HERDR_SOFT_SIGNAL_TOKENS를 넘으면 세션에 알린다.
# 강제 종료도 마무리 지시도 아니다. 워커는 지금 작업을 계속하고, 정리는 작업 경계에서 Orchestrator가 정한다(managing-session-context 스킬).
# 비율이 아니라 토큰 수로 잰다. 같은 30%도 창 크기에 따라 양이 다르기 때문이다.
# 항상 0으로 끝내고 오류는 출력하지 않는다. 신호가 빠져도 작업을 막아서는 안 된다.
[ -n "${HERDR_ROLE:-}" ] || exit 0
exec 2>/dev/null

threshold="${HERDR_SOFT_SIGNAL_TOKENS:-300000}"
step="${HERDR_SOFT_SIGNAL_STEP:-100000}"
case "$threshold$step" in '' | *[!0-9]*) exit 0 ;; esac

input="$(cat)"
sid="$(printf '%s' "$input" | jq -r '.session_id // ""')"
transcript="$(printf '%s' "$input" | jq -r '.transcript_path // ""')"
event="$(printf '%s' "$input" | jq -r '.hook_event_name // ""')"
agent="$(printf '%s' "$input" | jq -r '.agent_id // ""')"
[ -z "$agent" ] || exit 0 # 서브에이전트에는 알리지 않는다
case "$sid" in '' | *[!A-Za-z0-9_-]*) exit 0 ;; esac
[ -f "$transcript" ] || exit 0

tokens=""
for bytes in 1048576 8388608; do
  line="$(tail -c "$bytes" "$transcript" | grep -a '"type":"assistant"' | grep -a '"usage"' | tail -n 1)"
  [ -n "$line" ] || continue
  tokens="$(printf '%s' "$line" | jq -r '.message.usage | (.input_tokens // 0) + (.cache_read_input_tokens // 0) + (.cache_creation_input_tokens // 0)' | head -n 1)"
  break
done
case "$tokens" in '' | *[!0-9]*) exit 0 ;; esac

# herdr-report가 보고에 붙이도록 현재 크기를 남긴다.
if [ -n "${HERDR_PROJECT:-}" ]; then
  mkdir -p "$HOME/.claude/herdr/handoff/$HERDR_PROJECT" && printf '%s\n' "$tokens" >"$HOME/.claude/herdr/handoff/$HERDR_PROJECT/.$HERDR_ROLE.ctx"
fi

state_dir="$HOME/.claude/herdr/ctx-signal"
state="$state_dir/$sid"
mkdir -p "$state_dir" || exit 0
if [ "$tokens" -lt "$threshold" ]; then
  rm -f "$state" # compact·clear로 내려가면 다음에 다시 알린다
  exit 0
fi
last="$(cat "$state" 2>/dev/null)"
case "$last" in '' | *[!0-9]*) ;; *) [ "$tokens" -ge $((last + step)) ] || exit 0 ;; esac
printf '%s\n' "$tokens" >"$state"

kilo=$((tokens / 1000))
case "$HERDR_ROLE" in
  commander) todo="사용자에게 보고할 내용을 마무리하고 컨텍스트 정리를 사용자에게 요청한다." ;;
  orchestrator) todo="처리 중인 보고까지 마치고, 새 배정은 정리 뒤로 미뤄 상태판에 대기로 적고, 세션 장부를 고친 뒤 Commander에 [CLEAR 요청]을 보낸다." ;;
  *) todo="지금 작업은 수용 기준까지 계속한다. 맥락이 차면 자동 compact가 처리하므로 이 신호 때문에 서두르거나 미완성 작업을 [완료]로 보고하지 않는다. 신호만으로 /clear를 요청하지 않는다." ;;
esac
message="[컨텍스트 소프트 신호 ${kilo}k] ${todo} 규칙: managing-session-context 스킬."
jq -nc --arg e "$event" --arg m "$message" '{hookSpecificOutput: {hookEventName: $e, additionalContext: $m}}'
exit 0
