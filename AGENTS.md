# 에이전트 지침 — zip-viewer (Codex·Claude 공통)

Codex는 이 파일을 직접 읽고, Claude Code는 `CLAUDE.md`의 `@AGENTS.md`로 같은 내용을 읽는다. 표시 사이의 내용은 `harness sync`가 덮어쓰므로 고치지 않는다.

<!-- harness:begin -->
## 하네스 기본 규칙

- 역할: role이 있는 세션은 `.agents/skills/running-herdr-autopilot/SKILL.md`로 그 역할을 맡는다. 전역 지침이 `~/.agents/skills/operating-herdr-sessions`를 가리켜도 이 저장소에서는 읽지 않는다. role이 없으면 단일 작업 세션으로 일하며 herdr 스킬은 쓰지 않는다.
- 시작: 프로젝트의 목적·경계·승인 원칙은 `intent.md`로 먼저 확인한다.
- 직접 처리: 작업은 본 세션에서 직접 한다. 서브에이전트는 의존성 없는 검색과 구현에 대한 독립 리뷰에만 쓴다.
- 상태: 다음 세션에 필요한 것은 커밋, 상태판, 작업 상태 파일에 남긴다. 대화에만 있는 내용은 사라진다고 가정한다.
- 승인: 외부에 보이거나 되돌리기 어려운 변경, 파괴적 작업, 범위 확대는 사용자 승인 없이 하지 않는다.
- 확인: 주석, 문서, 보고, 요약은 단서다. 동작을 판단할 때는 코드를 읽거나 실행해 확인한다.
- 완료: 검증 근거 없이 완료를 선언하지 않는다. 보고의 검증은 실행 확인(명령과 결과), 추론, 미확인으로 구분한다. 추론과 미확인은 통과가 아니다. 실제 사용 경로 대신 다른 경로로 확인한 것(단위 테스트만, mock만)과 결론이 나지 않은 확인도 통과가 아니다. 직접 돌릴 수 있는 확인을 사용자에게 넘기지 않는다.
- 변경: 수용 기준을 채우는 가장 작은 변경을 한다. 범위 안의 불필요한 코드는 추가하기 전에 지운다. 증거가 뒷받침하지 않는 변경은 되돌린다.
- 주석: 코드로 표현할 수 없는 이유, 외부 제약, 공개 API 계약, 라이선스만 쓴다. 코드 동작 설명, 작업 경과, 주석 처리한 코드는 남기지 않는다. 강제할 수 있는 제약은 타입, 테스트, 검사로 옮긴다. 이번 변경에 닿는 코드에만 적용하고, 틀린 기존 주석은 고친다.
- 타입: 타입 검사가 가능한 프로젝트는 불가능한 상태를 타입으로 막고, 외부 입력은 경계에서 파싱하고, 스키마가 있으면 타입을 거기서 만든다. 타입 검사는 검사 명령에 넣는다. 검사하지 않는 타입 힌트는 주석과 같다.
- 언어: 보고·인계·사용자 응답·세션 간 메시지·커밋 메시지·PR 본문은 한국어로 쓴다.

## 스킬: 필요할 때만 읽는다

아래 작업을 시작하기 직전에 프로젝트의 `.agents/skills/<스킬>/SKILL.md`를 읽고 따른다. 그 밖의 시점에는 읽지 않는다.

| 작업 | 스킬 |
| --- | --- |
| role이 있는 세션의 역할 수행, 세션 띄우기, 배정·보고, 서브에이전트 사용 판단 | `running-herdr-autopilot` |
| 컨텍스트 알림 대응, `/clear`, compact 복구, 재개 | `managing-session-context` |
| `docs/` 문서를 읽거나 쓰기, 결정 기록, 작업 계획·상태판 | `writing-project-docs` |
| 코드·테스트를 작성하거나 고치기(TDD, 뮤테이션 확인) | `implementing-with-tdd` |
<!-- harness:end -->

## 프로젝트 규칙

- 주업무: 압축 해제 없이 ZIP·RAR·이미지 폴더·PDF 컬렉션을 브라우저에서 읽는 스캔→서재→뷰어 경로. 상세는 docs/service-plan/index.md
- 검사 명령: 전체 `make lint && make test && make web-gates`(web/node_modules가 없으면 먼저 `cd web && pnpm install --frozen-lockfile`). 모든 게이트는 `make gates`(실제 미디어가 없으면 `GATES_E2E=0`). 편집 직후 빠른 검사는 `scripts/quick-check.sh <파일>`(gofmt, eslint). 하네스 관문은 `.agents/harness.env`의 `HERDR_CHECK_CMD`·`HERDR_CHECK_FILE_CMD`가 같은 명령을 쓴다
- 실제 동작 확인: 합성 라운드 `make e2e-synthetic`(실 컬렉션은 `make e2e`, 둘 다 임시 서버·상태로 돌고 :9909 사용자 서버는 건드리지 않는다). 증거는 `SHELF_E2E_STATE`(기본 `/tmp/shelf-e2e-<pid>`)의 `server.log`와 `web/playwright-report/`. `docs/e2e-last-run.log`는 2026-07-31 기록이라 최신 증거가 아니다
- 코드 위치 찾기: graft(`graft ask "<작업>" --source`, `graft grep "<문자열>"`, `graft callers 함수이름`)
- 크기 예산 조정: 릴리스 크기 예산(`Makefile` `SIZE_BUDGET`)이 64 MiB(67,108,864 B) 미만으로 남는 조정은 docs/decisions.md E-78 기준(정식 측정 근거, E-78 §5 조정 기록)에 따라 사용자 승인 없이 정하고, 64 MiB 이상은 사용자 승인을 받는다. 조정할 때는 `Makefile`·`docs/impl-plan.md`·`README.md` 세 곳을 한 커밋에서 함께 바꾸고 `internal/buildinfo/release_budget_test.go`를 통과시킨다
