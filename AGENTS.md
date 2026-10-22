# AGENTS.md — SHELF 작업 지침 (Codex)

SHELF(제품명 Oh My Manhwa-bbang)입니다. 디스크에 쌓인 ZIP·RAR·폴더·PDF 만화/도서를 **압축 해제 없이** 읽는 웹 리더이며,
Go 단일 정적 바이너리(`dist/shelf`)에 React/Vite SPA를 embed 합니다. 개요는 `README.md`를 봅니다.

**기본 지침: 주업무는 시스템 구현·개발입니다. 데이터(개별 만화 파일) 복구는 과제가 아닙니다** (`.claude/rules/05-development-first.md`).
**절차는 작업 크기를 따릅니다. 시작할 때 티어(T0~T3)를 한 줄로 말합니다** (`.claude/rules/00-orchestration.md`).

세션을 시작하면 `docs/HANDOFF.md`부터 읽습니다. 문서가 충돌하면 `docs/prd.md`가 이기고, 전체 우선순위는 HANDOFF §3에 있습니다.

규칙의 원본은 `.claude/rules/` 하나입니다(Claude와 공용). Codex는 `CLAUDE.md`의 `@` import를 자동 로드하지 않으므로,
실질적인 작업을 시작하기 전에 아래 파일을 **번호 순서대로 모두 읽고** 저장소 작업 지침으로 적용합니다.

## 지침 목록 (번호 순으로 읽기)

| 파일 | 다루는 사안 |
| --- | --- |
| `.claude/rules/00-orchestration.md` | 작업 크기 티어(T0~T3), 오케스트레이터 컨텍스트 보호, 위임·병렬 실행, 비상 정지 |
| `.claude/rules/05-development-first.md` | **기본 지침** — 시스템 개발 우선, 데이터 복구는 범위 밖, 명세 변경은 판정으로 |
| `.claude/rules/10-model-routing.md` | 작업 성격별 서브에이전트 모델 지정 |
| `.claude/rules/15-tdd.md` | 상세 구현은 TDD — 실패 테스트 먼저, 기대값은 명세에서, 뮤테이션 자가검증 |
| `.claude/rules/20-review-cycle.md` | 구현·리뷰·개선 사이클, 게이트 선택, 리뷰 실행자 선택 |
| `.claude/rules/30-workflow.md` | 멀티에이전트 오케스트레이션 적용 기준과 검증 원칙 |
| `.claude/rules/40-reporting.md` | 사용자 보고 원칙 |
| `.claude/rules/50-environment.md` | 환경 제약과 안전 수칙 |

## Codex에서 읽을 때

- 규칙의 서브에이전트, 모델 지정(opus/haiku), `Agent`·`Workflow` 도구는 Claude Code 기준입니다.
  Codex에서는 같은 원칙(분석·구현·실행 분리, 오케스트레이터는 결론만 보유)을 쓸 수 있는 수단으로 따르고, 수단이 없으면 직접 수행합니다.
- `20-review-cycle.md`의 codex 리뷰 명령은 Claude가 구현했을 때의 외부 리뷰 경로입니다.
  Codex가 구현한 변경은 같은 세션이 리뷰하지 않습니다. 구현 맥락이 없는 새 Codex 세션으로 리뷰하고, 보고에 외부 관점 리뷰가 아님을 밝힙니다.

## 지침을 추가·수정할 때

- 규칙은 `.claude/rules/`에서만 고칩니다. 새 파일을 만들면 이 파일의 표와 `CLAUDE.md`의 표·`@` import를 함께 갱신합니다.
- 한 파일에는 한 사안만 둡니다. 파일명은 `<번호>-<사안>.md`, 번호는 10 단위로 띄웁니다.
- 규칙 간 참조는 섹션 번호가 아니라 파일명으로 합니다.
- 이 파일은 얇은 인덱스로 유지합니다.
