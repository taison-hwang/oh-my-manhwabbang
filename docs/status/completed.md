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
| U-R2a | U-R | main e8f983e. scripts/deploy/library(preflight·tunnel-apply·rollback·dns-route·verify-public·예시 설정)과 런북 docs/progress/2026-11-09T03-39-10+0900--e74-library-deploy-runbook.md. 사본 시험, 실제 /etc 무변경. 검사 PASS 8d0b280 |
| P-72 | P-72 | main e0c7be6. E-72 Docker 로컬: Dockerfile(scratch·USER 65532·CA)·.dockerignore·make docker(원격 이름 거부)·README. 검사 PASS 059dead, 독립 검증 PASS 059dead(/tmp/verify-p72/2026-11-09T06-01-37+0900--verify-p72.md), rebase 충돌 없음 |
| U-C | U-BC | main 69a39d6. E-74 user.db v4 사용자별 데이터(user_* 테이블, principal 이음매, index 진행률 사용자 범위, 정리·기동 복구 사용자별, 공유 표지 단일 판정, 이관·restore-owner, 내보내기 본인 범위). 검사 PASS dd5fb31·f0972f7, 독립 검증 ISSUES(UC-1·UC-2) → 재검증 PASS f0972f7 |
| U-B | U-BC | main c36823f. E-74 oidc 인증 코어(uni-auth Authorization Code+PKCE, BFF 서버 세션 auth.db, 인가 allowed/admin_subs, 재인증, 로그아웃, 사전 인증·JWKS 제한, return 정규화, 소유자 이관 실패 시 로그인 거부·늦은 지정 세션 삭제). 독립 검증 ISSUES 6 → 재검증 ISSUES 1 → 재검증 PASS 86e1305(/tmp/verify-ub/2026-11-09T08-32-50+0900--verify-ub3.md). 크기 +159,744 B로 예산 약 78 KB 초과(make release만, 사용자 결정 대기) |
| F-1 | F-1 | main ad9e0cf. 화살표 U+2190-2193 벤더 서브셋 웹폰트 Shelf Arrows(400·700, 각 약 1 KB), 모든 글꼴 스택 선두, fontCoverage 조인 검사·e2e CDP 단정, 픽셀 기준 82장(화살표 78 + e45 4, e45는 F-1 전후 바이트 동일·원인 미확인). 서체 NOTICE·OFL 원문(기존 벤더 서체 선행 누락 포함). 검사 PASS ab43bfa, 독립 검증 ISSUES(고지·개수) → 1c0f236 텍스트만, Orchestrator 대조. 크기 +4,096 B |
| U-D | U-D | main c68fd0b. E-74 SPA 인증 흐름: oidc 401은 session_expired만 start?return 자동 이동(이유 없는 401은 base_path/, §2.7 보정), 동시 401 1회·10초 루프 가드, 403 reason 4분기·재인증 대화상자, 로그아웃 두 폼, 비관리자 관리자 UI 숨김(권한 지연 도착 시 열린 확인·폼 닫힘). 검사 PASS 513c3a8·c33927b, 독립 검증 ISSUES(U-D-1) → 재검증 PASS c33927b(/tmp/verify-ud/2026-11-09T10-26-09+0900--verify-ud2.md) |
| P-71b | P-71 | main d32d2d8. E-71 RAR 5 비solid·단일·비암호화를 kind rar로(internal/archive/rar5, source 시그니처 분기, 창 64 MiB·CRC 없음 거부, 디코더 패닉 guard→ErrDecode), mkfixture v04, 픽셀 기준 6장(size-desc 4 = 울프가이 4권, palette 4a·4b laptop = main 변화 추정·원인 미확인). 검사 PASS 0fdb531, 독립 검증 PASS(/tmp/verify-p71b/2026-11-09T10-44-16+0900--verify-p71b.md). 크기 +36,864 B |
| P-70 | P-70 | main 456ec9b. E-70 뷰어 단면·양면 핀치 줌, 확대 중 팬, 가운데 36% 더블탭 확대/복귀, 확대 중 스와이프는 팬(배율 1에서만 쪽 넘김), 세로 모드 제외. e2e 13-gesture(터치 에뮬레이션, 진행률 결정적 정리). 검사 PASS 384dac7, 독립 검증 PASS(/tmp/verify-p70/2026-11-09T10-19-23+0900--verify-p70.md). SPA gzip +1.0 kB. 실기기 확인은 사용자 몫(미확인) |
