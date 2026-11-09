# 상태판

갱신: 2026-11-09T21:51:21+09:00 (Orchestrator t7c, U-R4 배정)

| 작업 ID | 묶음 | 담당(role·터미널) | 상태 | 의존 | 근거 |
| --- | --- | --- | --- | --- | --- |
| U-R3 | U-R3 | worker-3(t93), verifier-1(t7h) | U-R3a 병합 128a0f6, U-R3b 병합 a4da3b4, U-R3c는 사용자 적용 대기. 사용자 명령: docs/progress/2026-11-09T18-34-31+0900--e74-books-ops-prep.md §5 A~F | 없음 | 시작 계약 3: 운영 주소 books.betterax.net. a 저장소 → b 운영 준비(uni-auth 갱신·DNS·사용자 명령) → 사용자 적용 → c 공개 검증. U-R2b(library)는 이것으로 대체 |
| U-R4 | U-R4 | worker-3(t93, 보기 w6:pR), verifier-1(t9f, 보기 w6:pS) | 보완 3 커밋 5314f69 검사 중(뒤에서) + 런북 재검증 중(00:0x) | 없음 | 시작 계약 4. 같은 SHA에 검사 PASS·판정 PASS면 [병합] |

## 대기

| 작업 ID | 기다리는 것 | 시작 | 풀릴 조건 | 만료 | 만료 시 |
| --- | --- | --- | --- | --- | --- |
| U-R4 | 검사(5314f69, 뒤에서)·재판정(verifier-1 t9f) | 00:0x | 검사 PASS + [판정] PASS 5314f69 | 01:00 (검사 최대 20m, 재검증 20m, 1.5배 넘게) | hc read t9f·herdr-queue로 진행 확인 → 연장 또는 4절 |
| U-R3c | 사용자 결정·실행: §5 A~F(설정·:9909 중지·9910 기동·sudo ingress·재시작·verify·첫 로그인 allowed_subs) | 19:2x | 사용자가 적용 알림 → worker-3 U-R3c 공개 검증 + verifier-1(정리 후) 독립 검증 | 만료 없음(사용자 결정) | 결정 없이 할 일: 없음 |
| 회원관리 | 사용자 결정: 회원관리 확장 A(현 구조 유지+후속 2건)/B(스크립트 허용 목록 편집)/C(앱 내 회원관리, E-74 변경) | 22:0x | Commander 경유 답 | 만료 없음(사용자 결정) | 결정 없이 할 일: 없음(빈틈 2건은 A 확정 뒤 배정) |

## 세션 사고와 교체 (2026-11-09)

- 14:11 Commander가 옛 Orchestrator t1j를 `hc kill` → t1j가 띄운 워커·검증자·게이트 터미널이 함께 종료됐다. `herdr-session`은 `--outlive-parent` 없이 띄운 세션을 부모와 함께 끝낸다(OS 부모 사슬과 무관). 커밋은 모두 보존.
- 새 Orchestrator t7c(--outlive-parent로 기동). 이후 워커는 모두 `--outlive-parent`로 띄운다 — Orchestrator를 다시 교체해도 진행 중 작업이 살아남게.
- 옛 세션 정리 끝: t1j·t1m·t20·t3g·t5h·t6v 모두 종료 상태. worker-2 교체(기록 저장 세션)는 재기동으로 함께 끝났다.

## 새 Orchestrator가 이어 갈 다음 행동 (19:3x, t7c [CLEAR 요청] 직전)

1. (끝남 a4da3b4) U-R3b 병합·기록 커밋.
2. U-R4(21:51 배정, worker-3): [완료] 또는 검사 중(뒤에서) 보고가 오면 verifier-1을 `.agents/bin/herdr-session --outlive-parent`로 새로 띄워 [검증] 배정(분기점 03ec8c4·head SHA·시작 계약 4 완료 조건). 같은 SHA에 검사 PASS·판정 PASS면 [병합]. 병합 뒤 시작 계약 4 완료를 Commander에 [완료]로 보고.
2-1. 사용자가 §5(docs/progress/2026-11-09T18-34-31+0900--e74-books-ops-prep.md) A~F 적용을 알리면: worker-3에 U-R3c(공개 검증 verify-public --public 결과 확인, 비인증 401·allowed 밖 403·평문 우회 없음·실제 로그인 1회는 사용자 브라우저) 배정, verifier-1(t7h 19:4x 종료됨 → 실행기로 새로 띄움)에 독립 공개 검증 배정(읽기 전용 GET만, uni-auth·터널 쓰기 금지). 둘 다 PASS면 시작 계약 3 완료 → E-74 전체 완료 보고.
3. 사용자 결정 대기(Commander에 보냄): 공개 시점, main push(130+커밋), 옛 worktree 5개·.backup-harness/·damaged-zip-salvage 삭제, Codex 단가 기준, E-70 실기기 확인, §5 A의 allow_root_editing 유지 여부.
4. 하네스 규칙은 1967a2f 기준: 무거운 검사는 공용 대기열 herdr-queue, herdr-report --bg의 '검사 중(뒤에서)'도 병행 검증 계기. 워커는 모두 --outlive-parent.

## 미확정 사항

- 사용자 결정 대기: 3차 E-73·E-75~E-77은 승인 아님. 토큰 집계 질문 (3) Codex 단가 기준(사용자 답 없으면 계속 미산출).
- 승인 대기: main 미push(origin/main c2e1dfd 대비 다수 커밋), 옛 worktree 5개(.worktrees/e2e-54·e69-20/25/45/ea), `.backup-harness/`, `damaged-zip-salvage` 삭제.
- 사용자 확인 항목: E-70 실기기 핀치·더블탭 체감(CDP 에뮬레이션만 확인).
- 전량 test-int-full 미실행(사용자 결정으로 생략). 대신 유형별 대표 15건 격리 읽기 검증(f452721).
- 후속 후보(배정 안 함): oidc 중간 제거 403 화면에 sub 미표시(middleware.go:276)·빈 allowed_subs 전원 거부 직접 테스트 없음(회원관리 결정 A면 배정), 19:4x 대기 세션 3개 SIGTERM 종료 원인 미확인(Commander 지시 아님, hc ls --all exit 143·signal 15), 11c laptop-1024 팔레트 4a·4b 흔들림(같은 base에서 렌더 두 상태, U-E에서 감시 제외) — 배경 표지 준비를 기다려 결정적으로 만든 뒤 감시 복귀, salvage 잘린 마지막 엔트리(docs/progress/2026-11-08T22-59-30+0900--targeted-read-verification.md), docs/impl-plan.md:277 RAR 문장, 픽셀 기준이 썸네일만 남겨 원인 추적 불가(e45·palette 4a/4b), 13-gesture waitForTimeout(400) 2곳·양면 경계 영구 테스트, rar4 압축 경로 패닉 방어, Any.pick 8 B 추가 읽기, 응답 헤더 상한 분리, kbd 키 이름 시스템 monospace, U-C 실패·재시도 회귀 테스트 편입, 스캔 정리 N+1, fontTools 버전 고정, uni-auth post_logout 투영 결함(사용자에게 보고함).
- 운영 메모: herdr-report는 herdr-check(약 13분)를 돌리는 무거운 명령이라 heavy.lock 아래서만 부른다. 병합 완료·대기 보고는 hc로만(메인 체크아웃에서 herdr-report를 부르면 잠금 없이 전체 검사가 돈다 — 12:1x 사고). 보고 프로세스를 죽이면 자식 검사가 고아로 남으니 프로세스 묶음째 정리한다. 다른 프로젝트(ohmystock·uni-auth) 세션이 잠금 밖에서 무거운 검사를 돌려 메모리가 출렁인다 — 시작 가드를 MemAvailable ≥ 10 GB·load1 < 20로 둔다.
- 하네스 메모: 일부 워커가 Orchestrator 주소를 t1j로 잡아 hc 답장이 실패한다(ListAgents에 없음). 쉬는 워커에 메시지를 보내면 턴 종료 훅 때문에 보고가 되돌아오므로 배정 없이는 보내지 않는다.
