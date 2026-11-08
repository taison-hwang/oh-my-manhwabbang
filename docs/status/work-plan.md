# 작업 계획

갱신: 2026-11-09T02:55:45+09:00

## 시작 계약 (E-74 계정 기능, 2026-11-09)

요청(사용자, Commander t1h 경유 01:4x): "계정기능 진행. Commander가 제시한 uni-auth 추천안(Q1~Q12 추천)과 프로젝트 경계 변경을 승인으로 받아 판정·계약·intent를 확정하고 구현·독립 검증·병합까지 자율 진행. uni-auth 클라이언트 등록도 포함하되 환경·설정값이 불명확하면 안전하게 확인하고, 불가역·범위 초과는 결정 요청. 다른 3차 기능 승인으로 추정 금지."
근거 초안: docs/progress/2026-11-08T23-31-21+0900--e74-uni-auth-contract-draft.md (Q1~Q12 추천안 = 승인안).

- 완료 조건:
  1. E-74가 docs/decisions.md에 BINDING으로, A-20이 arch·impl-plan §0.3에, prd NFR-SEC-002·NFR-OPS-002와 intent.md(핵심 가치 5, 경계, 3차 다섯→넷)가 개정되고 `make lint`(contractcheck) PASS.
  2. `auth.mode: oidc`가 구현된다: 가짜 OIDC 공급자 테스트로 PKCE·state·nonce·login CSRF·토큰 검증 8항(EdDSA·kid·iss·aud·시각·tid·nonce·sub 일치)·allowed_subs 403 not_allowed·admin_only·reauth_required·cross_origin·로컬/통합 로그아웃·허용 목록 제거 시 차단·return 오픈 리다이렉트 방지가 통과하고, password·무인증 모드 기존 테스트가 그대로 통과.
  3. user.db v4 사용자별 분리: 사용자 A의 진행률·권별 설정·설정·보기 상태가 B에게 보이지 않고, 표지·시리즈 방향은 공유, legacy_owner_sub 1회 이관과 역복사 명령(Q12), 내보내기·가져오기 본인 범위가 테스트로 통과.
  (Q9 변경: 사용자 01:5x 승인으로 공개 터널 library.betterax.net)
  4. SPA: 401 → top-level 재인가, 로그아웃 버튼 2종과 uni-auth UI 계약 문구, 403 reason 안내가 동작하고 합성 e2e(가짜 공급자)가 통과.
  5. 최종 main에서 `make lint && make test && make web-gates`와 `make e2e-synthetic` PASS, 크기 예산 안, 독립 검증 PASS 뒤 병합.
  6. uni-auth 클라이언트 등록(사용자 승인, 운영 주소 https://library.betterax.net)과 Cloudflare 터널 ingress: 등록은 바로, 터널 공개는 oidc 서버의 인증·접근 제한·우회 경로 검증 뒤. 실제 로그인 1회 확인. 기존 서비스 설정 보존.
- 허용 범위: zip-viewer 코드·테스트·docs(intent 포함, 위 승인 범위), 새 Go 의존성 없이 표준 라이브러리. uni-auth는 읽기·GET만, 클라이언트 등록 1건만 사용자 URL 확정 뒤. 금지: push, :9909 재시작·쓰기, 다른 3차 기능, 크기 예산 상향, uni-auth 설정 변경·로그인 시도(등록 전).
- 탈출 조건: 계약이 승인안 밖의 결정을 요구할 때, 크기 예산 초과, 재배정 2번+재계획 실패, uni-auth 쓰기가 등록 1건을 넘을 때.

## 시작 계약 2 (3차 E-70·E-71·E-72, 2026-11-09)

요청(사용자, Commander t1h 경유 02:5x): "모바일 제스처(E-70), 포맷 확장(E-71), Docker(E-72)를 먼저 진행해도 좋다. 세 기능 착수와 필요한 기존 경계 변경 승인. 계정 기능과 병렬로 계획·구현·검증. 포맷은 실제 사례와 seek 제약을 조사해 범위를 정하고, 수용 기준으로 결정 가능한 선택은 자율 결정. Docker는 로컬 빌드·실행까지, 게시 금지. 다른 3차 기능(E-73·E-75~77)은 승인 아님."
근거 초안: docs/progress/2026-11-08T22-43-17+0900--phase3-decision-drafts.md E-70·E-71·E-72.

- 완료 조건:
  1. E-70·E-71·E-72가 docs/decisions.md에 BINDING, intent.md 경계(:37·:39)와 impl-plan §1.3 OUT 행·prd(E-71이면 §7.2)·arch(필요한 A-n)가 개정되고 `make lint` PASS.
  2. E-70: 뷰어 단면·양면 핀치 줌·확대 중 팬·더블탭 확대/복귀, 확대 중 가로 스와이프는 팬(배율 1에서만 페이지 넘김), 기존 탭 영역·스와이프 규칙(E-28) 회귀 없음 — Playwright 터치 에뮬레이션 테스트 PASS. 실기기 확인은 사용자 몫으로 보고(미확인 표기).
  3. E-71: 실컬렉션 읽기 전용 측정(.7z·RAR 5 수, solid·분할·암호화 비율)과 핵심 가치 3(한 쪽 = 한 번의 seek) 대조로 범위를 정한다. solid·암호화는 거부 유지가 기본. 범위 안 포맷은 픽스처·오라클 테스트로 열리고, 범위 밖은 이유를 밝힌 unsupported. 새 의존성·예산 상향이 필요하면 그 부분만 [차단].
  4. E-72: Dockerfile·.dockerignore·make docker(로컬 빌드), 컨테이너 기동 스모크(헬스, 설정 경로, 미디어 :ro 문서화), oidc 모드용 CA 인증서 포함. 게시 없음.
  5. 각 코드 묶음 독립 검증 PASS 뒤 병합, 최종 main 전체 검사 PASS, 크기 예산 안.
- 허용 범위: web/src(E-70), internal/archive·scan·index kind(E-71), Dockerfile·Makefile·README·docs(E-72). 금지: 레지스트리 게시, push, :9909 조작, 다른 3차 기능, 새 런타임 의존성·예산 상향(승인 필요).
- 탈출 조건: 범위 결정이 수용 기준으로 정해지지 않을 때(E-71 7z 의존성 등), 크기 예산 초과.
- 묶음: P-D(판정 문서 E-70·71·72, 문서만) → P-70(SPA 제스처) · P-71(측정 → 구현) · P-72(Docker). P-70은 U-D(SPA 인증)와 web/src를 공유하므로 파일 겹침을 배정 때 나눈다. P-72는 E-74 oidc와 무관하게 진행하되 CA 인증서는 넣는다.

## 의존 묶음

- U-A(계약 확정, 첫 묶음): 문서만. worker-2.
- U-BC(백엔드): U-B 인증 코어(internal/auth/oidc·auth.db·핸들러·관문·설정) → U-C user.db v4·저장소 사용자 키·이관·내보내기. U-A 병합 뒤 worker-2.
- U-D(SPA): web/src/features/auth 등. U-A 병합 뒤 worker-1(정리 후), U-BC와 파일 겹침 없음(계약 A-20으로 연결).
- U-E(e2e·마감): 가짜 공급자 합성 e2e, gates, 크기. U-BC·U-D 병합 뒤.
- U-R(uni-auth·터널): U-R1 환경 확인·클라이언트 등록(worker-1, 지금) → U-R2 터널 적용·공개 전 검증(U-BC·U-D 병합 뒤).

## 작업

| 작업 ID | 묶음 | 내용 | 수용 기준 | 의존 | 바꿀 파일·모듈 |
| --- | --- | --- | --- | --- | --- |
| U-A | U-A | E-74·A-20 확정, prd·intent·arch·impl-plan 개정 | 완료 조건 1 | 없음 | docs/decisions.md, intent.md, docs/prd.md, docs/arch-backend.md, docs/impl-plan.md |
| U-B | U-BC | oidc 인증 코어 | 완료 조건 2 | U-A | internal/auth/**, internal/httpapi/(auth·router·loginpage), internal/config |
| U-C | U-BC | user.db v4·사용자별 저장소 | 완료 조건 3 | U-B | internal/userdata/**, internal/repair, internal/httpapi(progress·prefs·settings·export) |
| U-D | U-D | SPA 인증 흐름 | 완료 조건 4(화면) | U-A | web/src/** |
| U-E | U-E | 합성 e2e(가짜 공급자)·gates·크기, make release 측정 크기(MEASURED_*·README) 재측정 | 완료 조건 4(e2e)·5 | U-BC, U-D, P-70, P-71b | web/e2e, scripts/e2e*, scripts/mkfixture, Makefile MEASURED_* |
| U-R1 | U-R | 환경 확인(cloudflared·Hydra admin·:9909 설정), 클라이언트 등록 1건, 터널 적용 계획 | 기록 문서, 등록 GET 대조 | 없음 | docs/progress/ 새 파일 |
| U-R2 | U-R | (배치 기준: oidc 단일 인스턴스 127.0.0.1) 사용자가 실행할 백업·적용·검증·복구 스크립트와 절차 준비 → 사용자 실행 뒤 공개 전 우회 경로 검증, 실제 로그인. sudo·재시작·공개는 사용자 | 비인증 요청이 모든 데이터 경로에서 401/로그인, 허용 목록 밖 403, 실제 로그인 성공 | U-BC, U-D, U-R1 | cloudflared 설정(백업 후) |
| P-D | P-D | E-70·E-71·E-72 판정 확정, intent 경계·impl-plan·prd·arch 개정 | 시작 계약 2 완료 조건 1 | 없음(E-71 범위는 P-71a 측정 뒤 보정) | docs/decisions.md, intent.md, docs/impl-plan.md, docs/prd.md, docs/arch-backend.md, docs/ui-spec.md |
| P-71a | P-71 | 실컬렉션 포맷 측정(읽기 전용)·seek 제약 조사·범위 제안 | 측정 기록 docs/progress | 없음 | docs/progress 새 파일 |
| P-71b | P-71 | E-71 범위 구현 | 완료 조건 3 | P-D, P-71a | internal/archive/**, internal/scan, scripts/mkfixture |
| P-70 | P-70 | 모바일 제스처 | 완료 조건 2 | P-D | web/src/features/viewer/** |
| P-72 | P-72 | Docker 로컬 | 완료 조건 4 | P-D | Dockerfile, .dockerignore, Makefile, README |
