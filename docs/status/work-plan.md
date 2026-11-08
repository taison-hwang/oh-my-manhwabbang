# 작업 계획

갱신: 2026-11-09T02:10:21+09:00

## 시작 계약 (E-74 계정 기능, 2026-11-09)

요청(사용자, Commander t1h 경유 01:4x): "계정기능 진행. Commander가 제시한 uni-auth 추천안(Q1~Q12 추천)과 프로젝트 경계 변경을 승인으로 받아 판정·계약·intent를 확정하고 구현·독립 검증·병합까지 자율 진행. uni-auth 클라이언트 등록도 포함하되 환경·설정값이 불명확하면 안전하게 확인하고, 불가역·범위 초과는 결정 요청. 다른 3차 기능 승인으로 추정 금지."
근거 초안: docs/progress/2026-11-08T23-31-21+0900--e74-uni-auth-contract-draft.md (Q1~Q12 추천안 = 승인안).

- 완료 조건:
  1. E-74가 docs/decisions.md에 BINDING으로, A-20이 arch·impl-plan §0.3에, prd NFR-SEC-002·NFR-OPS-002와 intent.md(핵심 가치 5, 경계, 3차 다섯→넷)가 개정되고 `make lint`(contractcheck) PASS.
  2. `auth.mode: oidc`가 구현된다: 가짜 OIDC 공급자 테스트로 PKCE·state·nonce·login CSRF·토큰 검증 8항(EdDSA·kid·iss·aud·시각·tid·nonce·sub 일치)·allowed_subs 403 not_allowed·admin_only·reauth_required·cross_origin·로컬/통합 로그아웃·허용 목록 제거 시 차단·return 오픈 리다이렉트 방지가 통과하고, password·무인증 모드 기존 테스트가 그대로 통과.
  3. user.db v4 사용자별 분리: 사용자 A의 진행률·권별 설정·설정·보기 상태가 B에게 보이지 않고, 표지·시리즈 방향은 공유, legacy_owner_sub 1회 이관과 역복사 명령(Q12), 내보내기·가져오기 본인 범위가 테스트로 통과.
  4. SPA: 401 → top-level 재인가, 로그아웃 버튼 2종과 uni-auth UI 계약 문구, 403 reason 안내가 동작하고 합성 e2e(가짜 공급자)가 통과.
  5. 최종 main에서 `make lint && make test && make web-gates`와 `make e2e-synthetic` PASS, 크기 예산 안, 독립 검증 PASS 뒤 병합.
  6. uni-auth 클라이언트 등록: 사용자가 zip-viewer 접속 URL(redirect_url·audience)을 정하면 등록하고 실제 로그인 1회로 확인. 정해지지 않으면 등록 요청서(값·명령)만 남기고 미완료로 보고.
- 허용 범위: zip-viewer 코드·테스트·docs(intent 포함, 위 승인 범위), 새 Go 의존성 없이 표준 라이브러리. uni-auth는 읽기·GET만, 클라이언트 등록 1건만 사용자 URL 확정 뒤. 금지: push, :9909 재시작·쓰기, 다른 3차 기능, 크기 예산 상향, uni-auth 설정 변경·로그인 시도(등록 전).
- 탈출 조건: 계약이 승인안 밖의 결정을 요구할 때, 크기 예산 초과, 재배정 2번+재계획 실패, uni-auth 쓰기가 등록 1건을 넘을 때.

## 의존 묶음

- U-A(계약 확정, 첫 묶음): 문서만. worker-2.
- U-BC(백엔드): U-B 인증 코어(internal/auth/oidc·auth.db·핸들러·관문·설정) → U-C user.db v4·저장소 사용자 키·이관·내보내기. U-A 병합 뒤 worker-2.
- U-D(SPA): web/src/features/auth 등. U-A 병합 뒤 worker-1(정리 후), U-BC와 파일 겹침 없음(계약 A-20으로 연결).
- U-E(e2e·마감): 가짜 공급자 합성 e2e, gates, 크기. U-BC·U-D 병합 뒤.
- U-R(uni-auth 등록): 사용자 URL 결정 뒤.

## 작업

| 작업 ID | 묶음 | 내용 | 수용 기준 | 의존 | 바꿀 파일·모듈 |
| --- | --- | --- | --- | --- | --- |
| U-A | U-A | E-74·A-20 확정, prd·intent·arch·impl-plan 개정 | 완료 조건 1 | 없음 | docs/decisions.md, intent.md, docs/prd.md, docs/arch-backend.md, docs/impl-plan.md |
| U-B | U-BC | oidc 인증 코어 | 완료 조건 2 | U-A | internal/auth/**, internal/httpapi/(auth·router·loginpage), internal/config |
| U-C | U-BC | user.db v4·사용자별 저장소 | 완료 조건 3 | U-B | internal/userdata/**, internal/repair, internal/httpapi(progress·prefs·settings·export) |
| U-D | U-D | SPA 인증 흐름 | 완료 조건 4(화면) | U-A | web/src/** |
| U-E | U-E | 합성 e2e·gates·크기 | 완료 조건 4(e2e)·5 | U-BC, U-D | web/e2e, scripts/e2e*, scripts/mkfixture |
| U-R | U-R | uni-auth 클라이언트 등록·실제 로그인 확인 | 완료 조건 6 | 사용자 URL | 없음(uni-auth 1건) |
