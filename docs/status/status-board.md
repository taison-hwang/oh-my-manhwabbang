# 상태판

갱신: 2026-11-09T14:33:48+09:00 (Orchestrator t7c)

| 작업 ID | 묶음 | 담당(role·터미널) | 상태 | 의존 | 근거 |
| --- | --- | --- | --- | --- | --- |
| U-E | U-E | worker-2(새로 띄움) | 최종 게이트 재실행 | 없음 | work/u-e-finish 2c189a2(main d7bfc0b 위, 깨끗). 14:11 세션 사고로 final-gates4가 단계 시작 전 소실 → 새 worker-2가 herdr-check → e2e-synthetic → release 재실행 → [완료] → 독립 검증(orchestrator-drafts/verify-ue.txt) |

## 세션 사고와 교체 (2026-11-09)

- 14:11 Commander가 옛 Orchestrator t1j를 `hc kill` → t1j가 띄운 워커·검증자·게이트 터미널이 함께 종료됐다. `herdr-session`은 `--outlive-parent` 없이 띄운 세션을 부모와 함께 끝낸다(OS 부모 사슬과 무관). 커밋은 모두 보존.
- 새 Orchestrator t7c(--outlive-parent로 기동). 이후 워커는 모두 `--outlive-parent`로 띄운다 — Orchestrator를 다시 교체해도 진행 중 작업이 살아남게.
- 옛 세션 정리 끝: t1j·t1m·t20·t3g·t5h·t6v 모두 종료 상태. worker-2 교체(기록 저장 세션)는 재기동으로 함께 끝났다.

## 새 Orchestrator가 이어 갈 다음 행동

1. U-E [완료]가 오면 검사 PASS SHA를 확인하고 verifier-1(새로 띄움)에 독립 검증을 배정한다. 확인 항목: 가짜 uni-auth가 제품 바이너리에 링크되지 않음(go list -deps ./cmd/shelf), 제품 코드에 테스트용 우회 없음, 가짜 공급자의 aud 또는 nonce를 틀리게 하면 로그인 실패, oidc spec (a)~(h) 단정, 직접 뮤테이션 1개, make e2e-synthetic-oidc 1회, 예산 세 곳(Makefile·impl-plan·README) 33,816,576 일치와 정식 측정. PASS면 [병합](보고는 hc로만).
2. T-U 병합 완료(main 51178af).
3. U-E 병합 뒤 E-74 완료 조건 6(운영 공개) 준비: 사용자 실행 단계 U-R2b — 런북 docs/progress/2026-11-09T03-39-10+0900--e74-library-deploy-runbook.md와 scripts/deploy/library/. 사용자에게 적용 명령과 중단 시점을 요청한다(:9909 중지, user.db 백업, 9910 oidc 인스턴스, dns-route, tunnel-apply --restart는 auth·ohmyassets가 몇 초 끊김, verify-public, 첫 로그인 403 화면의 sub를 allowed_subs에). sudo·재시작·cloudflared는 사용자가 한다.
4. '개발완료!!!'는 U-E 병합(E-74 조건 1~5)과 T-U 병합 뒤 Commander에 알릴 수 있다. 운영 공개(조건 6, 실제 로그인 1회)는 사용자 실행이 필요하다고 함께 밝힌다. 미완료를 완료로 쓰지 않는다.

## 미확정 사항

- 사용자 결정 대기: 3차 E-73·E-75~E-77은 승인 아님. 토큰 집계 질문 (3) Codex 단가 기준(사용자 답 없으면 계속 미산출).
- 승인 대기: main 미push(origin/main c2e1dfd 대비 다수 커밋), 옛 worktree 5개(.worktrees/e2e-54·e69-20/25/45/ea), `.backup-harness/`, `damaged-zip-salvage` 삭제.
- 사용자 확인 항목: E-70 실기기 핀치·더블탭 체감(CDP 에뮬레이션만 확인).
- 전량 test-int-full 미실행(사용자 결정으로 생략). 대신 유형별 대표 15건 격리 읽기 검증(f452721).
- 후속 후보(배정 안 함): salvage 잘린 마지막 엔트리(docs/progress/2026-11-08T22-59-30+0900--targeted-read-verification.md), docs/impl-plan.md:277 RAR 문장, 픽셀 기준이 썸네일만 남겨 원인 추적 불가(e45·palette 4a/4b), 13-gesture waitForTimeout(400) 2곳·양면 경계 영구 테스트, rar4 압축 경로 패닉 방어, Any.pick 8 B 추가 읽기, 응답 헤더 상한 분리, kbd 키 이름 시스템 monospace, U-C 실패·재시도 회귀 테스트 편입, 스캔 정리 N+1, fontTools 버전 고정, uni-auth post_logout 투영 결함(사용자에게 보고함).
- 운영 메모: herdr-report는 herdr-check(약 13분)를 돌리는 무거운 명령이라 heavy.lock 아래서만 부른다. 병합 완료·대기 보고는 hc로만(메인 체크아웃에서 herdr-report를 부르면 잠금 없이 전체 검사가 돈다 — 12:1x 사고). 보고 프로세스를 죽이면 자식 검사가 고아로 남으니 프로세스 묶음째 정리한다. 다른 프로젝트(ohmystock·uni-auth) 세션이 잠금 밖에서 무거운 검사를 돌려 메모리가 출렁인다 — 시작 가드를 MemAvailable ≥ 10 GB·load1 < 20로 둔다.
- 하네스 메모: 일부 워커가 Orchestrator 주소를 t1j로 잡아 hc 답장이 실패한다(ListAgents에 없음). 쉬는 워커에 메시지를 보내면 턴 종료 훅 때문에 보고가 되돌아오므로 배정 없이는 보내지 않는다.
