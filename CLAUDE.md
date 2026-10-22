# CLAUDE.md — SHELF 작업 지침

SHELF(제품명 Oh My Manhwa-bbang). 디스크에 쌓인 ZIP·RAR·폴더·PDF 만화/도서를 **압축 해제 없이** 읽는 웹 리더다.
Go 단일 정적 바이너리(`dist/shelf`)에 React/Vite SPA를 embed 한다. 개요는 `README.md`.

**기본 지침: 주업무는 시스템 구현·개발이다. 데이터(개별 만화 파일) 복구는 과제가 아니다** (`.claude/rules/05-development-first.md`).
**절차는 작업 크기를 따른다. 시작할 때 티어(T0~T3)를 한 줄로 말한다** (`.claude/rules/00-orchestration.md`).

세션을 시작하면 `docs/HANDOFF.md`부터 읽는다. 문서가 충돌하면 `docs/prd.md`가 이긴다. 전체 우선순위는 HANDOFF §3이 원본이다.

작업 지침은 사안별로 `.claude/rules/` 에 분리되어 있다.
이 파일은 **인덱스이자 로더**이며, 아래 `@` import 로 각 규칙 파일이 세션에 자동 로드된다.

## 지침 목록

| 파일 | 다루는 사안 |
| --- | --- |
| `.claude/rules/00-orchestration.md` | 작업 크기 티어(T0~T3), 오케스트레이터 컨텍스트 보호, 위임·병렬 실행, 비상 정지 |
| `.claude/rules/05-development-first.md` | **기본 지침** — 시스템 개발 우선, 데이터 복구는 범위 밖, 명세 변경은 판정으로 |
| `.claude/rules/10-model-routing.md` | 작업 성격별 서브에이전트 모델 지정 (분석·설계·리뷰 / 구현 / 실행) |
| `.claude/rules/15-tdd.md` | 상세 구현은 TDD — 실패 테스트 먼저, 기대값은 명세에서, 뮤테이션 자가검증 |
| `.claude/rules/20-review-cycle.md` | 구현 → 리뷰 → 개선 사이클, 게이트 선택, codex 외부 리뷰 실행 규칙 **(리뷰 실행자 택일의 규범 원본)** |
| `.claude/rules/30-workflow.md` | ultracode / Workflow 멀티에이전트 적용 기준, **다관점 검증·적대적 검증 예외 경계의 규범 원본** |
| `.claude/rules/40-reporting.md` | 사용자 보고 원칙 |
| `.claude/rules/50-environment.md` | 환경 제약과 안전 수칙 — 읽기 전용 미디어 볼륨, 사용자 서버, Go 환경, 공유 작업 트리 |

@.claude/rules/00-orchestration.md
@.claude/rules/05-development-first.md
@.claude/rules/10-model-routing.md
@.claude/rules/15-tdd.md
@.claude/rules/20-review-cycle.md
@.claude/rules/30-workflow.md
@.claude/rules/40-reporting.md
@.claude/rules/50-environment.md

## 지침을 추가·수정할 때

- **한 파일 = 한 사안.** 성격이 다른 규칙을 기존 파일에 끼워넣지 말고 새 파일로 만든다.
- 파일명은 `<번호>-<사안>.md`. 번호는 10 단위로 띄워 두었으므로 사이에 삽입할 여지가 있다.
- 새 파일을 만들면 **위 표와 `@` import 목록, `AGENTS.md`의 목록에 반드시 함께 추가**한다.
  import 를 빠뜨리면 파일은 존재하지만 로드되지 않는다.
- 규칙 간 참조는 섹션 번호(§3) 대신 **파일명**으로 한다. 파일이 재배치돼도 참조가 깨지지 않는다.
- 이 파일 자체는 얇게 유지한다. 실제 규칙 본문은 항상 `.claude/rules/` 안에 둔다.
- `.claude/rules/`는 저장소에 커밋된다. `.gitignore`는 `.claude/settings.local.json`과 `.claude/worktrees/`만 무시한다.
