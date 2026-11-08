# 상태판

갱신: 2026-11-09T03:36:18+09:00

| 작업 ID | 묶음 | 담당(role·터미널) | 상태 | 의존 | 근거 |
| --- | --- | --- | --- | --- | --- |
| U-C | U-BC | verifier-1(t20) | 검증 | 없음 | dd5fb31 검사 PASS, 독립 검증 배정 2026-11-09T03:36:18+09:00 |
| U-R2a | U-R | worker-1(t1k) | 진행 | 없음 | 공개 적용 스크립트·런북 준비, 배정 2026-11-09T03:36:18+09:00 |
| U-B | U-BC | worker-2(t1m) | 진행 | 없음 | 배정 2026-11-09T02:58:04+09:00, 이음매 internal/principal·router Options.PerUser |

## 미확정 사항

- 사용자 결정 대기: 3차 기능 착수 범위(E-74는 uni-auth 사용으로 결정, Q1~Q12와 intent 변경 승인 대기, 나머지 7기능은 결정 없음).
- 승인 대기: main 미push(origin/main c2e1dfd 대비 다수 커밋), 옛 worktree 5개(.worktrees/e2e-54·e69-20/25/45/ea, 원본 사본 /tmp/zip-viewer-e69-originals는 재부팅 시 소실), `.backup-harness/`, `damaged-zip-salvage` 삭제.
- 전량 test-int-full 미실행(사용자 결정으로 생략). 대신 유형별 대표 15건 격리 읽기 검증(f452721).
- 후속 후보(판정 전): salvage가 잘린 마지막 엔트리를 쪽으로 잡아 요청 시 본문 도중 연결이 끊김(국수집 10권·유레카26 원본, 보관함에만 있음). docs/progress/2026-11-08T22-59-30+0900--targeted-read-verification.md.
- 후속 후보: docs/impl-plan.md:277 RAR 문장이 D-71 이후 틀림.
- 운영 메모: herdr-report는 herdr-check(약 13분)를 돌리는 무거운 명령이라 heavy.lock 아래서만 부른다. 보고 프로세스를 죽이면 자식 검사가 고아로 남으니 프로세스 묶음째 정리한다.
| P-72 | P-72 | worker-3(t3f) | 진행 | 없음 | P-D 병합 35496ca, 시작 2026-11-09T03:36:18+09:00 |
| P-71a | P-71 | worker-4(t3g) | 진행 | 없음 | 배정 2026-11-09T02:57:45+09:00, 이어서 E-71 판정·P-71b |
