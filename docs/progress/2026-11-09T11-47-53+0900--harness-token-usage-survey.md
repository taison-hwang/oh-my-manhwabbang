timestamp: 2026-11-09T11:47:53+09:00
topic: harness-token-usage-survey
type: investigation
status: done

# R-T — 새 하네스 적용 이후 토큰 사용량·비용 집계 방안

질문: 하네스 설치 커밋 `0461718`(2026-11-08 22:13:38 +0900, 이하 기준 시점) 이후 이 프로젝트(zip-viewer)의 토큰 사용량 통계를 낼 방법이 있는가. 읽기 전용 조사이며 설정·환경·하네스 파일은 바꾸지 않았다. 다른 프로젝트(ohmystock 등) 기록은 경로·cwd로 구분만 하고 집계하지 않았다. 수치와 메타데이터만 적었고 대화 본문·비밀값은 옮기지 않았다.

표기: **[실행]** 명령을 돌려 확인, **[추론]**, **[미확인]**. 모든 시각은 KST, 기록의 `timestamp`는 UTC(`Z`)라 +9h로 환산했다.

## 결론 요약

1. **지금 기록으로 집계되는 것은 일부다.** Claude는 worker-3·worker-4(02:57 KST 이후)의 transcript만 디스크에 있다. Orchestrator·worker-1·worker-2는 transcript가 **아예 저장되지 않는다**(원인 확정, 아래 1절). 기준 시점~02:57 구간의 Claude 워커 기록도 없다. Codex(Commander·verifier-1)는 전량 있다.
2. **중복 집계가 크다.** Claude는 응답 하나가 content block마다 usage를 반복 기록해 원시 합계가 약 1.9배로 부풀려진다. `message.id`로 중복을 제거해야 한다.
3. **비용은 Claude 쪽만 공식 단가로 환산 가능**하다(기록된 부분 약 $39). Codex 단가는 확인하지 못했다. 둘 다 구독 사용이면 실비가 아닌 "API 단가 환산 비용"이다.
4. 방안은 (A) 읽기 전용 집계 스크립트, (B) 워커 transcript 저장 켜기, (C) 하네스 보고에 usage 붙이기. 추천은 B로 빠진 구간을 막고 A로 집계하는 조합이다. B·C는 하네스 변경이라 사용자 승인이 필요하다.

## 1. 기록 현황과 빠진 세션 [실행]

### 1.1 transcript가 없는 이유 — 확정
`/proc/<pid>/environ`(변수 이름만 확인)에서 현재 살아 있는 zip-viewer 세션을 대조했다.

| 세션(pid, 시작) | `CLAUDE_CODE_CHILD_SESSION` | 디스크 jsonl |
| --- | --- | --- |
| orchestrator (421577, 11-08 22:17) | 1 | 없음 |
| worker-1 (482853, 22:36) | 1 | 없음 |
| worker-2 (489766, 22:38) | 1 | 없음 |
| worker-3 (3848020, 11-09 06:25) | 없음 | 있음(10fd85d9 → 17b4fe09) |
| worker-4 (2488150, 02:57) | 없음 | 있음(c020a16e → da0c1641 → 790cc26c → 201da00d) |

- 앞의 셋은 같은 부모 세션 마커(`CLAUDE_CODE_SESSION_ID=5d4d9faa…`)를 상속했다. 이 세션 화면의 "Transcript saving is off — inherited CLAUDE_CODE_CHILD_SESSION marker" 경고와 일치한다. 이 세션(worker-1, `a2d93d90`)은 `~/.claude/session-env/`에는 있으나 어느 `projects/*/` 아래에도 jsonl이 없다.
- 하네스 쪽 증거: `herdr-report`가 붙이는 `ctx` 값은 transcript를 읽는 hook(`.agents/hooks/context-signal.sh`)이 `.agents/…/.<role>.ctx`에 남기는데, 이 파일이 `~/.claude/herdr/handoff/zip-viewer/`에 `.worker-3.ctx`·`.worker-4.ctx`만 있고 orchestrator·worker-1·worker-2는 없다. 같은 원인이다.
- 흔적만 남은 세션: 같은 프로젝트 폴더에 main jsonl 없이 `tool-results/`·`subagents/*.meta.json`만 있는 세션 ID가 4개(`ff430782` 22:38~03:33, `c0ad58bf` 02:15~03:02, `c47980f0` 06:07~06:22, `cc4aecb4` 08:34~11:24). 서브에이전트 meta 7건이 있으나 jsonl이 없어 사용량은 알 수 없다. [추론] 위 세 세션의 `/clear` 연쇄로 보이나 역할과의 1:1 대응은 transcript가 없어 확정하지 못했다.

### 1.2 Claude jsonl 구조 (zip-viewer)
- 경로: `~/.claude/projects/-mnt-data-codejobs-projects-betterax-proto-zip-viewer/<sessionId>.jsonl`, 서브에이전트는 `<sessionId>/subagents/agent-*.jsonl`(+`.meta.json`: agentType, description, model).
- `type:"assistant"` 줄의 `message.usage`: `input_tokens`, `output_tokens`, `cache_creation_input_tokens`(+`cache_creation.ephemeral_5m/1h_input_tokens`), `cache_read_input_tokens`, `service_tier`, `speed`; `message.model`; 최상위 `timestamp`, `sessionId`, `cwd`.
- 역할은 SessionStart hook이 넣은 "역할은 worker-N" 문구로 확인했다(c020a16e·da0c1641·790cc26c·201da00d=worker-4, 8a11bc78·10fd85d9·17b4fe09=worker-3). 프로세스 시작 시각·모델·ledger와도 맞는다.

## 2. 집계 결과 (기준 시점 이후, 중복 제거) [실행]

방법: `python3 -I`로 jsonl을 읽기 전용으로 순회, `timestamp >= 2026-11-08T13:13:38Z`, `message.id`로 중복 제거(원시 1502줄 → 고유 804응답; 같은 id의 usage 값은 전부 같음을 c020a16e 133개 id에서 확인), 서브에이전트 jsonl 포함.

### 2.1 역할·모델별 (Claude)
| 역할 | 모델 | 응답 | input | output | cache 생성 | cache 읽기 |
| --- | --- | --- | --- | --- | --- | --- |
| worker-3 | sonnet-5-5 | 255 | 570 | 117,122 | 355,876 | 34,425,550 |
| worker-3 | opus-5-5(서브에이전트) | 33 | 68 | 27,394 | 90,900 | 2,432,262 |
| worker-4 | opus-5-5 | 516 | 1,080 | 362,406 | 965,093 | 84,883,481 |
| **합계** | | 804 | 1,718 | 506,922 | 1,411,869 | 121,741,293 |

- cache 읽기가 전체의 98% 이상이다. input_tokens는 거의 0이라 "입력 토큰"만 보면 사용량을 크게 놓친다.
- 메인 세션 cache 생성은 전부 1시간 TTL, 서브에이전트는 전부 5분 TTL이었다(합계가 정확히 일치: 1,162,407 / 249,462). 단가가 달라 비용 계산에 TTL 구분이 필요하다.
- 서브에이전트 합계: 102응답, output 76,925, cache 읽기 10,850,339.

### 2.2 세션·작업별 (worktree 이름을 작업의 대리 지표로 사용)
세션: worker-4 `c020a16e`(133+서브69) → `da0c1641`(186) → `790cc26c`(107) → `201da00d`(21), worker-3 `8a11bc78`(124+서브33) → `10fd85d9`(125) → `17b4fe09`(6).

| 역할 | worktree(작업) | 응답 | output | cache 생성 | cache 읽기 | 시간대 |
| --- | --- | --- | --- | --- | --- | --- |
| worker-3 | p-d-docs (P-D) | 58 | 46,869 | 152,086 | 5,055,481 | 02:57~03:35 |
| worker-3 | p-72-docker (P-72) | 32 | 16,124 | 33,773 | 4,527,896 | 03:35~04:30 |
| worker-3 | p-70-gesture (P-70) | 171 | 72,790 | 175,628 | 24,425,950 | 04:30~10:56 |
| worker-4 | p-71-formats (P-71) | 44 | 30,233 | 85,876 | 7,170,597 | 03:06~04:05 |
| worker-4 | p-71b-rar5 (P-71b) | 219 | 169,438 | 395,790 | 35,266,500 | 03:15~10:44 |
| worker-4 | f-1-arrows (F-1) | 165 | 103,423 | 226,284 | 31,067,877 | 05:21~08:49 |
| worker-4 | r-b-budget (R-B) | 20 | 14,367 | 47,982 | 1,771,549 | 11:35~11:46 |
| 둘 다 | (본 체크아웃) | 105 | 56,285 | 301,199 | 13,079,797 | 전 구간 |

- 이 대응은 메시지의 직전 `cwd`가 `.worktrees/<이름>` 아래인지로 정한 근사이다. 본 체크아웃에서 한 일(조사·병합·문서)은 작업에 못 붙는다.
- 정확한 대응이 필요하면 `docs/status/decisions.tsv`(84행 중 76행이 worker/터미널 ID를 증거에 적음)와 시각으로 붙인다. 작업 ID(P-70, F-1…)와 세션 ID를 직접 잇는 필드는 없다.

### 2.3 일자별
기록된 Claude 사용은 전부 11-09 02:57 이후라 일자별로는 11-09 한 줄이다(위 합계). 11-08 22:13~11-09 02:57의 Claude 워커 사용은 기록이 없다.

### 2.4 Codex (cwd가 zip-viewer인 rollout만) [실행]
경로 `~/.codex/sessions/2026/11/{08,09}/rollout-*.jsonl`. `session_meta.cwd`로 이 프로젝트만 걸러 80개 중 기준 시점 이후 기록이 있는 12개를 집계했다(다른 프로젝트 196개는 열거·집계하지 않음). `token_usage_record`(응답별 usage)를 `response_id`로 중복 제거해 더한 값이 각 rollout 마지막 `token_count.total_token_usage`와 12개 모두 일치했다.

| 역할(추론) | 모델 | rollout | input | (그중 cached) | output | (그중 reasoning) |
| --- | --- | --- | --- | --- | --- | --- |
| Commander | gpt-6.1-terra / medium | 1 | 3,869,555 | 3,698,688 | 12,050 | 797 |
| verifier-1 | gpt-6.1-sol / high | 11 | 54,308,434 | 53,160,704 | 290,419 | 116,506 |
| **합계** | | 12 | 58,177,989 | 56,859,392 | 302,469 | 117,303 |

- OpenAI 방식이라 `cached_input_tokens`는 `input_tokens`의 부분집합, `reasoning_output_tokens`는 `output_tokens`의 부분집합이다(`total_tokens` = input + output으로 확인). 캐시 생성 토큰은 0으로만 기록된다(`cache_write_input_tokens`).
- 역할은 [추론]이다: 모델(terra=Commander, sol=verifier), 프로세스 시작 시각(410796=22:14:42 ↔ rollout 22-14-44, 1685429=00:28:09 ↔ 00-28-12)이 맞는다. rollout에 역할 필드는 없다. verifier-1은 `/clear`·재시작마다 rollout이 새로 생겨 11개로 나뉜다.
- 작업별 대응은 Codex rollout에 worktree/작업 필드가 없어 배정 시각과 대조해야 한다 [미확인].
- 일자: 11-08 22:14~ 입력 2.24M(terra 부분), 11-09 55.9M.

## 3. 한계

- **기록 없는 세션**: Orchestrator·worker-1·worker-2 전량과 기준 시점~02:57 구간의 Claude 워커(옛 worker-3 t1q 등). 합계를 "프로젝트 전체 사용량"으로 쓰면 안 된다. 특히 Orchestrator(opus/high)는 가장 오래 사는 세션이라 누락 비중이 클 것으로 [추론]되나 크기는 [미확인].
- **`/clear`·compact로 끊긴 구간**: Claude는 `/clear`마다 sessionId가 바뀌고 파일이 새로 생긴다(잇는 필드 없음, 프로세스 시작 시각·ledger로 연결). compact는 같은 파일 안에서 이어지고 요약 호출이 별도 응답으로 기록되는지는 확인하지 못했다 [미확인]. Codex는 세션마다 별도 rollout.
- **서브에이전트**: CHILD_SESSION 세션의 서브에이전트는 jsonl이 없다(meta만). worker-3·4 것은 `subagents/`에 있어 포함했다. 서브에이전트와 부모 사이에 같은 `message.id`가 겹치는지 합산 단계에서 확인했고 겹침이 없어 이중 집계는 없다.
- **중복 집계**: Claude는 `message.id`, Codex는 `response_id`로 제거. Codex `token_count`는 누적값이라 합산하지 말고 마지막 값만 쓴다. 원시 합산은 Claude 기준 약 1.9배 과대.
- **시계**: Claude는 UTC, Codex도 UTC(`Z`), 하네스 파일 mtime·ledger는 KST. 일자 경계는 KST 기준으로 맞춰야 한다.
- **혼입**: `~/.claude/herdr/ctx-signal/`·`session-env/`는 프로젝트 구분이 없고 ohmystock 세션 ID가 섞여 있다(2945e581, 09e64b7f, e6082b43, cfb4b890 등). 반드시 `projects/<경로>/`·`session_meta.cwd`로 거른다. Codex는 같은 날짜 폴더에 두 프로젝트가 섞인다.
- **worktree cwd**: 작업별 집계는 직전 cwd 기준 근사.
- **ctx 값은 사용량이 아니다**: 보고에 붙는 `ctx Nk`는 마지막 응답의 input+cache(맥락 크기)이고 compact 횟수는 `.orchestrator.compacts`뿐이라, 누적 사용량 복원에는 못 쓴다. `herdr-report`·hook은 usage를 로그로 남기지 않는다(grep 확인).

## 4. 비용 집계 가능 여부

**Claude (공식 단가 확인함)** — 출처: https://platform.claude.com/docs/en/about-claude/pricing (2026-11-09 조회, 표 값 그대로). USD/MTok:

| 모델 | input | output | cache 쓰기 5m | cache 쓰기 1h | cache 읽기 |
| --- | --- | --- | --- | --- | --- |
| Claude Opus 5.5 | 4 | 20 | 5 | 8 | 0.20 |
| Claude Sonnet 5.5 | 2 | 10 | 2.50 | 4 | 0.10 |

- 2.1의 기록분에 TTL별 쓰기 단가를 적용하면 [실행] **약 $39.15** (worker-4 opus $31.54, worker-3 sonnet $6.12, worker-3 서브에이전트 opus $1.49). 이는 위 기록된 804응답만의 값이다.
- 이 세션은 `--dangerously-skip-permissions` 대화형 CLI라 구독(정액) 사용이면 실제 청구액이 아니고 **API 단가 환산 비용**이다. 구독 여부 자체는 기록에서 확인하지 못했다 [미확인]. 가격표 쪽 `fast` 속도 할증은 기록의 `speed`/`service_tier`가 전부 `standard`여서 해당 없음.

**Codex (미확인)** — gpt-6.1-sol·gpt-6.1-terra의 공식 단가를 찾지 못했다. https://openai.com/api/pricing/ 은 HTTP 403이라 열리지 않았다. rollout의 `rate_limits`에 `plan_type`·주간 `used_percent`·`credits`(has_credits=false)가 있어 구독 한도로 쓰는 것으로 보이나, 토큰당 실비 환산은 단가 출처를 확보하기 전까지 하지 않는다. 사용 비율(`used_percent`)은 비용이 아니라 한도 소진율이다.

## 5. 구현 방안

| | 내용 | 필요한 변경 | 승인 |
| --- | --- | --- | --- |
| **A. 읽기 전용 집계 스크립트** | 위 조사와 같은 방식(`python3 -I`, 중복 제거, 기준 시점 필터, cwd/세션→역할·작업 매핑, 단가표 파일 분리)을 `scripts/`에 둔다. 출력은 표(역할·모델·작업·일자). | 새 파일 1개와 테스트. 기록 파일은 읽기만 | 저장소 변경 승인(일반 작업 범위). 비용은 Claude만, Codex 단가는 확보 후 |
| **B. 워커 transcript 저장 켜기** | `herdr-session`이 세션을 띄울 때 상속된 `CLAUDE_CODE_CHILD_SESSION`을 제거하거나 `CLAUDE_CODE_FORCE_SESSION_PERSISTENCE=1`을 넣는다(후자는 이 세션 화면 경고가 언급한 방법이며 **동작은 시험하지 않았다** [미확인]). 반영은 세션 재시작 때부터. | `.agents/bin/herdr-session`(및 하네스 원본 저장소) 환경 정리 한 줄. 이미 떠 있는 Orchestrator·worker-1·2는 재시작 전까지 계속 기록 없음 | **필요**: 하네스 파일 변경 + 세션 재시작. 부수효과로 `ctx` 값과 소프트 신호 hook도 이 세션들에서 동작하기 시작 |
| **C. 보고에 usage 붙이기** | `herdr-report`가 `[완료]`마다 세션 usage 합계(또는 이전 보고 이후 증분)를 `ctx`처럼 덧붙이거나 `handoff/…/usage.tsv`에 한 줄씩 남긴다. | `.agents/bin/herdr-report` 수정, transcript가 있어야 읽을 수 있음(= B 선행). Codex는 rollout을 읽는 코드 별도 | **필요**: 하네스 변경. 작업 ID↔usage가 보고 시점에 직접 묶인다는 이점 |

추천: **B → A**. B 없이는 A의 합계가 계속 부분 집합이다. C는 작업별 정확한 대응이 꼭 필요할 때만 후속으로 한다. 지금 당장 부분 통계가 필요하면 A만 먼저 만들어 "worker-3·4·Codex만 집계됨, 나머지 미집계"를 출력에 명시한다.

## 6. 재현 명령 (읽기 전용)
집계 스크립트는 저장소에 넣지 않았다(스크래치 전용). 핵심 규칙만 적는다.
- Claude: `type=="assistant"`, `message.usage` 존재, `timestamp >= 2026-11-08T13:13:38Z`, `message.id`로 dedupe, 메인 `*.jsonl`과 `*/subagents/*.jsonl` 모두 순회, 합계 필드 4개.
- Codex: `session_meta.cwd`가 이 저장소 경로로 시작하는 rollout만, `token_usage_record.payload.usage`를 `response_id`로 dedupe(검산: 마지막 `token_count.info.total_token_usage`).
- 실행 확인 범위: 위 수치는 이 조사 시점(2026-11-09 11:4x)에 열려 있던 파일이며, 실행 중인 세션은 계속 늘어난다.

## 위험·미확인 정리
- [미확인] Orchestrator·worker-1·worker-2·옛 워커의 사용량 크기, compact 요약 호출의 기록 여부, B 방법의 실제 동작, 구독 여부, Codex 단가.
- [추론] Codex 역할 대응, 가려진 세션 ID 4개의 역할.
