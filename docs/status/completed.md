# 완료 작업

| 작업 ID | 묶음 | 결과·근거 |
| --- | --- | --- |
| E69-1 | 표지 편집 | b3468ab main 병합. 병합 후 herdr-check PASS. [구현·검증 기록](../progress/2026-11-08T19-34-48+0900--e69-integration.md) |
| E69-2 | 표지 편집 | b3468ab 독립 검증 PASS, make gates 5/5. /tmp/verify-e69/2026-11-08T20-58-01+0900--verify-e69.md |
| R-D1~3 | R-D | main ac210f6(42b4ee4·ac210f6) 병합, 검사 PASS 1faa020(브랜치)·22af509(병합 뒤 main). E-69 §4·§5 빈칸, HANDOFF §00, AGENTS 규칙 3칸 |
| R-R2 | R-R | main 22af509 병합, 검사 PASS 3c351b2. 3차 8기능 판정 초안 docs/progress/2026-11-08T22-43-17+0900--phase3-decision-drafts.md (proposed). 병합 [완료] 검사 PASS b128aa8 |
| R-V1 | R-V | 사용자 결정으로 취소. test-int-full 중단·정리(잔여 프로세스 0, .worktrees/r-v1 제거) |
| R-V3 | R-V | main f452721. 유형별 대표 15건 격리 읽기 검증: 통과 13·약한 통과 3·구조만 1·불일치 후보 2(기존 salvage 문제). 검사 PASS c892418 |
| R-F1 | R-F | main bdc919e. Writer 시계 주입으로 플레이크 테스트 결정화. 검사 PASS bdc919e, 독립 검증 PASS bdc919e(/tmp/verify-rf1/2026-11-09T00-30-10+0900--verify-rf1.md) |
| R-R3 | R-R | main c892418. E-74 uni-auth 연동 계약 초안(proposed) docs/progress/2026-11-08T23-31-21+0900--e74-uni-auth-contract-draft.md. 검사 PASS 2842af9·c892418 |
| U-R1 | U-R | main 528d61d. uni-auth 환경 확인(cloudflared·Hydra·:9909)과 클라이언트 zip-viewer 등록(201·projection ok·Hydra 대조), 기록 docs/progress/2026-11-09T02-17-19+0900--e74-uni-auth-tunnel-env.md. post_logout_redirect_uris Hydra 미투영(uni-auth 결함). 검사 PASS c2fe475 |
| U-A | U-A | main 7486524. E-74 BINDING·A-20 확정, intent·prd·arch·impl-plan 개정(uni-auth 위임 다중 사용자, library.betterax.net 공개 터널, 배치 기준). 검사 PASS 5c81e4a(rebase 내용 동일) |
| P-D | P-D | main 35496ca. E-70(제스처 최소안)·E-72(Docker 로컬) BINDING, intent 경계·impl-plan·arch·ui-spec 개정. 검사 PASS e713db8(rebase는 양쪽 살리기·E-74 문구만) |
| P-71a·E-71 문서 | P-71 | main d0d669a. 실컬렉션 26,970 파일 측정 기록, E-71 BINDING(RAR 5 비solid·단일·비암호화, 7z·분할 제외, solid·암호화 거부). 검사 PASS f206372(rebase는 양쪽 살리기) |
