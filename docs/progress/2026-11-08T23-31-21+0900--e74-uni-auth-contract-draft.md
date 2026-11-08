timestamp: 2026-11-08T23:31:21+09:00
topic: e74-uni-auth-contract-draft
type: decision
status: proposed
supersedes: docs/progress/2026-11-08T22-43-17+0900--phase3-decision-drafts.md

# E-74 다중 사용자·인증 — uni-auth 연동 계약 초안 (검토용, 미확정)

**대체 범위:** 이 기록은 위 `supersedes` 파일의 **E-74 절만** 대체한다. 같은 파일의 E-70~E-73, E-75~E-77은 그대로 유효하고, 그 추천들은 사용자가 승인하지 않았다. 번호 E-74와 개정 A-20은 가번호다. 사용자가 확정하기 전에는 `docs/decisions.md`·prd·intent·arch·코드 어느 것도 바뀌지 않는다(intent.md:27-28).

인용 기준: zip-viewer는 `main` 22af509, uni-auth는 저장소 `/mnt/data/codejobs/projects/betterax-proto/uni-auth`의 커밋 6fc19d2(`git show 6fc19d2:<경로>` 기준. 이 작업 중에도 다른 세션이 uni-auth 문서를 고치고 있어 작업 트리 행 번호는 움직일 수 있다). uni-auth 인용은 `uni-auth/<경로>:<행>`으로 쓴다. "추정"은 측정하지 않은 값이다.

## a. 사용자 결정

> "계정 시스템은 이 기계에서 돌고 있는 uni-auth를 쓴다" — 2026-11-08 23:2x, Commander(t1h) 경유로 Orchestrator가 전달.

이 결정으로 R-R2 초안 E-74의 대안 (a') 인스턴스 분리, (b') 비밀번호 없는 프로필, (b) 설정 파일 사용자 목록은 계정 원천으로서 기각된다. 남는 질문은 "uni-auth를 어떤 모양으로 붙이는가"다.

## b. uni-auth 연동 사실

### b.1 문서에서 확인한 계약

| 사실 | 출처 |
| --- | --- |
| 표준 OIDC 서버다. 프로토콜은 **Authorization Code + PKCE(S256) 하나뿐**이고 Implicit·ROPC·Hybrid는 없다 | uni-auth/docs/manual/integration-guide.md:28-31 |
| 연동하는 서비스는 **public 클라이언트**다. `client_secret`이 없고 PKCE가 대신한다 | uni-auth/docs/manual/integration-guide.md:39-40 |
| 시작점은 디스커버리 URL 하나다. 엔드포인트 경로는 전부 거기서 읽고 코드에 박지 않는다 | uni-auth/docs/manual/integration-guide.md:32-38 |
| 디스커버리는 기동 때 1회 받아 캐시한다(권장 TTL 1시간 이상) | uni-auth/docs/manual/integration-guide.md:249-250 |
| Access Token은 JWT, 수명 15분이다. JWKS를 캐시해 네트워크 없이 로컬 검증한다. 인증 서버가 죽어도 발급된 토큰은 만료까지 검증된다 | uni-auth/docs/manual/integration-guide.md:41-42 |
| 사용자 키는 `sub`다. `email`을 키로 쓰지 않는다 | uni-auth/docs/manual/integration-guide.md:43-44, :367 |
| AT 클레임: `sub`·`aud`·`sid`·`roles`·`tid`(항상 `"default"`, 검증할 것)·`iss`·`exp/iat/nbf`·`jti`. 표에 없는 클레임은 읽지 않는다 | uni-auth/docs/manual/integration-guide.md:363-377 |
| ID 토큰의 `aud`는 `client_id`이고 `nonce`가 있다. ID 토큰을 API Bearer로 쓰지 않는다 | uni-auth/docs/manual/integration-guide.md:379-391 |
| 검증 체크리스트 8개: 서명(`alg`는 `EdDSA`만)·`kid`로 키 선택·`iss` 완전 일치·`aud`·시각(±60초)·`tid`·ID 토큰 `nonce`·`sub`로 식별 | uni-auth/docs/manual/integration-guide.md:399-408 |
| JWKS 캐시 TTL은 AT 수명보다 길게(권장 30분). 모르는 `kid`면 1회 재조회하되 쿨다운(예 30초) | uni-auth/docs/manual/integration-guide.md:410-418 |
| `roles`: 전역 role은 접두어 없음(예 `platform_admin`), 서비스 role은 `<client_id>:<role>`. **서비스 role을 부여하는 API는 현재 없다** — 관리 API는 전역 `platform_admin` 지정·해제만 받는다 | uni-auth/docs/manual/integration-guide.md:152, :370 |
| 등록은 레코드 1건이다. 필수 4개는 `client_id`·`name`·`redirect_uris`·`audiences`. DCR은 없다 | uni-auth/docs/manual/integration-guide.md:94, :112-113, :120 |
| `redirect_uris`는 exact match다. `dev_mode=false`면 `https://`만 허용한다. 루프백 `http`는 `dev_mode=true` 승인 클라이언트에만 허용한다 | uni-auth/docs/manual/integration-guide.md:158-166 |
| 등록은 운영 팀이 한다. 서비스가 스스로 등록하는 경로는 없다. 연동 가이드는 관리 API 또는 SQL을 정식 경로로 적지만, 운영 가이드 기준으로 관리 API·`uni_auth.client` 테이블은 **아직 없고**, 지금 실제 경로는 Hydra admin `POST /admin/clients` 1회다 | uni-auth/docs/manual/integration-guide.md:150, uni-auth/docs/manual/operations-guide.md:603-607, :619 |
| 폐기 지연: 세션·토큰을 폐기해도 이미 발급된 AT는 최대 15분 유효하다. 서비스가 직접 발급한 세션 쿠키는 서비스 책임이다 | uni-auth/docs/manual/integration-guide.md:467-494 |
| 고위험 동작(관리자 기능 등)에서는 그 순간 재확인하라(`prompt=login`, 자체 세션 상태 조회 등) | uni-auth/docs/manual/integration-guide.md:496-507 |
| refresh token은 1회용·회전이다. 재사용하면 패밀리 전체가 폐기된다(유예 0초) | uni-auth/docs/manual/integration-guide.md:511-524 |
| 권장 저장 방식은 BFF다: 토큰은 서비스 서버가 보관하고, 브라우저에는 `HttpOnly; Secure; SameSite=Lax` 세션 쿠키(세션 ID만)를 준다. `code_verifier`·`state`·`nonce`도 서버 측에 둔다 | uni-auth/docs/manual/integration-guide.md:607-622 |
| 토큰을 `localStorage`·일반 쿠키·URL에 두지 않는다 | uni-auth/docs/manual/integration-guide.md:599-605 |
| iframe 무음 갱신은 불가능하다(인증 화면 `frame-ancestors 'none'`) | uni-auth/docs/manual/integration-guide.md:633-635 |
| 로그아웃 UI 계약(누락은 계약 위반): 버튼 2종 `"로그아웃"`(로컬)·`"모든 서비스에서 로그아웃"`(통합). 로컬 로그아웃 완료 화면에는 `다른 서비스에는 계속 로그인되어 있습니다.`를 반드시 넣는다. 로그인 폼을 서비스 도메인에 두지 않는다 | uni-auth/docs/manual/integration-guide.md:726-757 |
| 통합 로그아웃은 `end_session_endpoint`로 top-level 리다이렉트한다(`id_token_hint` 필수). 확인 화면이 뜨고, 취소하면 콜백이 오지 않는다. 로컬 로그아웃을 먼저 끝낸다 | uni-auth/docs/manual/integration-guide.md:766-802 |
| back-channel logout은 없다(P1, 설계 확정 후) | uni-auth/docs/manual/integration-guide.md:801, :887 |
| forward-auth·신뢰 헤더 모드는 연동 문서에 없다(`forward.auth`·`X-Forwarded-User`·`X-Auth-Request` grep 0건). 계약은 디스커버리에 광고된 엔드포인트가 전부다 | uni-auth/docs/manual/integration-guide.md:707 |
| B2C 통합 인증이고 사용자가 스스로 가입한다(가입 화면 `/registration`) | uni-auth/README.md:3, uni-auth/docs/manual/user-flows.md:118 |
| 공유 스택은 호스트의 `cloudflared` 터널이 `127.0.0.1:8443`으로 들어와 `https://auth.betterax.net`으로 공개된다 | uni-auth/docs/manual/install-guide.md:250 |

### b.2 실행 확인 (GET만, 2026-11-08 23:2x)

- `docker ps`: `uniauth-gateway-1`(`127.0.0.1:8443->8443`), hydra·kratos·postgres·valkey가 모두 `Up 5 days (healthy)`. `127.0.0.1:18100`은 uni-auth가 아니라 다른 앱(`ohmyassets-app-1`)이다.
- `GET https://127.0.0.1:8443/.well-known/openid-configuration` → **200**, 키 26개. 인증서는 자체 서명이다(curl `ssl_verify_result` 18). 주요 값:
  - `issuer` = `https://auth.betterax.net`. 엔드포인트도 전부 이 호스트다(`/authorize`·`/token`·`/jwks.json`·`/userinfo`·`/revoke`·`/end-session`).
  - `code_challenge_methods_supported` = `["S256"]`, `grant_types_supported` = `authorization_code`·`refresh_token`, `response_types_supported` = `["code"]`, `token_endpoint_auth_methods_supported` = `["none"]`.
  - `id_token_signing_alg_values_supported` = `["EdDSA"]`.
  - `claims_supported` = `sub, sid, roles, tid, email, email_verified, name, picture`. `scopes_supported` = `openid, offline_access, profile, email`.
  - `backchannel_logout_supported`·`frontchannel_logout_supported` = `false`. `registration_endpoint`·`introspection_endpoint`는 없다.
- `GET https://127.0.0.1:8443/jwks.json` → **200**, 키 2개. 둘 다 `OKP/Ed25519/EdDSA/use=sig`이고 `kid`가 있다. 키 값은 이 문서에 옮기지 않았다.
- `getent hosts auth.betterax.net` → 공인 IPv6 주소 2개(Cloudflare 대역으로 보임 — 추정). 브라우저와 zip-viewer 서버 모두 공인 인터넷을 거쳐 이 이름에 닿아야 한다.
- 헬스 스크립트(`uni-auth/gateway/health.sh`)는 compose 네트워크 안에서만 돌게 되어 있어 실행하지 않았다.

### b.3 미확인

- zip-viewer용 클라이언트는 아직 등록되지 않았다. 등록은 uni-auth 쓰기라 이 작업에서 하지 않았다.
- 실제 로그인·토큰 발급·`roles` 실값은 확인하지 않았다(로그인 금지).
- 인가 코드 TTL은 uni-auth도 미확정이다(uni-auth/docs/manual/integration-guide.md:313, :862).
- zip-viewer 서버(NAS·이 기계)에서 `https://auth.betterax.net`으로 나가는 연결이 되는지 시험하지 않았다(`getent`까지만).
- 다른 서비스(ohmyassets)의 연동 코드는 읽지 않았다(다른 프로젝트).

## c. 계약 초안

### c.1 모양: zip-viewer 서버가 BFF가 되고, oidc 모드는 서버 측 세션을 둔다

- zip-viewer는 Go 단일 바이너리가 SPA와 API를 함께 서빙하므로 uni-auth가 권장하는 BFF(uni-auth/docs/manual/integration-guide.md:607-622)가 맞는다. 브라우저는 토큰을 보지 않는다.
- 지금의 `shelf_session`은 서버 측 저장소 없는 자기완결 HMAC 쿠키다(arch-backend.md:3033). 이 모양으로는 다음 셋을 할 수 없다.
  - 사용자 한 명을 즉시 끊기(uni-auth/docs/manual/integration-guide.md:938 "계정 차단·로그아웃 시 서비스 자체 세션을 즉시 파기").
  - 서버 측 로그아웃.
  - 통합 로그아웃에 필요한 ID 토큰 보관.
- 그래서 **oidc 모드는 서버 측 세션 표를 쓴다.**
  - 쿠키 `shelf_sid`에는 무작위 세션 ID만 담는다(uni-auth/docs/manual/integration-guide.md:619 "쿠키 내용 | 세션 ID 만").
  - 세션 표는 새 파일 `<data_dir>/auth.db`에 둔다(c.6). 질문 Q6.
- **모드마다 쿠키를 나눈다.**
  - password 모드는 지금의 `shelf_session`(HMAC, `v:1`)을 그대로 쓰고 `shelf_sid`를 읽지 않는다.
  - oidc 모드는 `shelf_sid`만 읽고 `shelf_session`을 거부한다.
  - 지금 코드는 `v`가 1이 아닌 토큰을 이미 거부한다(internal/auth/session.go:80).
  - 효과: 모드를 바꿔 재시작해도 다른 모드의 쿠키가 통과하지 못한다.
- **권한은 쿠키에 싣지 않는다.** 허용 여부와 관리자 여부는 매 요청 현재 설정에서 판정한다(c.4·c.5).

### c.2 인증 흐름

모든 화면(로그인 버튼, 403 안내, 로그아웃 완료 고지)은 서버가 그린 HTML로 낸다. 인증 관문이 정적 자산까지 막아 SPA가 세션 없이 뜨지 않으므로, 지금의 서버 렌더 로그인 화면(internal/httpapi/loginpage.go)과 같은 방식을 쓴다.

1. **로그인 시작** `GET {base_path}/api/auth/oidc/start?return=<경로>`
   - 무작위 `code_verifier`·`state`·`nonce`와 사전 인증 ID를 만든다.
   - 사전 인증 ID를 쿠키 `shelf_login`으로 준다: `HttpOnly; Secure; SameSite=Lax; Max-Age=600; Path={base_path}/api/auth/oidc/`.
   - 서버 메모리 항목 `{state, 사전 인증 ID의 해시, verifier, nonce, return, prompt 폴백 플래그, 만료}`를 둔다. 상한은 다음과 같다.
     - 전체 항목 수(예: 1 000개, 추정값).
     - IP별 속도 제한(로그인과 별도 버킷, 예: 분당 20회, 추정값).
     - 10분 만료와 주기 정리.
     - 상한에 닿으면 `429 rate_limited`.
     - 재기동하면 진행 중이던 로그인만 실패한다.
   - `return`은 항목에 묶어 서버에 보관한다. 허용하는 값은 `{base_path}/`로 시작하는 경로뿐이다. `//`·`/\`·스킴·제어 문자가 있으면 `{base_path}/`로 바꾼다.
   - 디스커버리의 `authorization_endpoint`로 302. `scope=openid`, `redirect_uri`는 설정값 문자열 그대로다.
2. **콜백** `GET {base_path}/api/auth/oidc/callback?code&state`
   1. `error` 파라미터를 처리한다.
   2. **`state`와 `shelf_login` 쿠키가 같은 항목을 가리키는지** 대조한다(login CSRF 방지 — uni-auth/docs/manual/integration-guide.md:911이 서버 측 세션 보관을 요구한다).
   3. 항목을 1회용으로 표시한다. 같은 항목으로 콜백이 다시 오면(프리페치·재시도, uni-auth/docs/manual/integration-guide.md:314) 이미 만든 세션이 있을 때는 `return`으로 보내고, 없으면 처음부터 다시 시작하는 화면을 보인다.
   4. `/token` 교환: `client_id`는 본문에, `code_verifier`를 넣고, `redirect_uri`는 1단계와 같은 문자열을 쓴다(uni-auth/docs/manual/integration-guide.md:335).
   5. ID 토큰 검증: 8개 체크리스트, `aud`=`client_id`, `nonce` 대조.
   6. AT 검증: `aud`=설정 `audience`. **ID 토큰과 AT의 `sub`가 같아야 한다.**
   7. 허용 목록 판정(c.4).
   8. 세션 행을 만들고 `shelf_sid`를 발급한 뒤 `shelf_login`을 지운다. `return`으로 302.
   - 디스커버리의 `issuer`는 설정 `issuer`와 문자열이 같아야 한다. 다르면 로그인을 거부하고 로그를 남긴다.
   - 콜백 URL의 `code`·`state`와 모든 토큰은 로그에 남기지 않는다. 요청 로그(`log.http_requests`)에서는 이 경로의 쿼리를 지운다(uni-auth/docs/manual/integration-guide.md:605).
   - 허용 목록 밖이면 세션을 만들지 않고 403 안내 화면을 보인다. 화면에는 자신의 `sub`를 표시해 운영자가 설정에 추가할 수 있게 한다.
3. **세션 행** (auth.db `oidc_sessions`)
   - 열: `id_hash`(쿠키 값의 SHA-256, 원문은 저장하지 않음)·`user_sub`·`uni_sid`·`id_token`·`created_at`·`last_seen_at`·`reauth_at`·`expires_at`.
   - 쿠키 속성: `HttpOnly; Secure; SameSite=Lax; Path={base_path}/`. oidc 모드는 https가 전제라(c.7) `Secure`를 항상 붙인다.
   - **매 요청 검사:** 행이 있는가 → 만료 전인가 → `user_sub`가 현재 `allowed_subs`에 있는가. 없으면 행을 지우고 401(세션 없음)이나 403 `not_allowed`.
   - 사용자 한 명 끊기: 설정에서 `sub`를 빼면 다음 요청부터 거부된다. 재시작이 필요한지는 설정 재적재 방식에 따른다(지금 설정은 재시작 때 읽는다 — 추정). 즉시 끊는 관리자 끝점(`DELETE /api/auth/sessions?sub=`)은 최소안에 넣지 않는다.
   - **NTP 전제:** 서버 시계가 ±60초 안에 맞아야 토큰 검증이 된다(uni-auth/docs/manual/integration-guide.md:405). 운영 문서에 적는다.
4. **만료와 재확인** — 대안 셋, 추천은 (S2) (질문 Q3):
   - (S1) `session_ttl`만 쓴다(지금 기본 720h, arch-backend.md:423).
   - (S2) TTL을 짧게(예: 12h, 추정값) 두고 만료되면 재인가한다. SPA는 401을 받으면 `start?return=<현재 경로>`로 top-level 이동한다.
     - 서버는 만료된 세션 행이 남아 있을 때만 `prompt=none`을 붙인다. 첫 진입에는 붙이지 않는다(uni-auth/docs/manual/integration-guide.md:669).
     - `login_required`·`consent_required`가 오면 `prompt` 없는 요청으로 **한 번만** 폴백한다. 폴백 여부는 사전 인증 항목에 1회성 플래그로 기록한다(uni-auth/docs/manual/integration-guide.md:677).
     - 만료된 행은 N일(예: 30일) 뒤 지운다.
   - (S3) `offline_access`로 refresh token을 받아 auth.db에 보관하고 15분마다 갱신한다. 회전·재사용 탐지(uni-auth/docs/manual/integration-guide.md:511-524) 때문에 동시 갱신을 직렬화해야 한다.
5. **로그아웃** — uni-auth UI 계약(uni-auth/docs/manual/integration-guide.md:726-757)대로 버튼 2종을 둔다.
   - `"로그아웃"` = `POST {base_path}/api/auth/logout`(지금 경로). 세션 행을 지우고 쿠키를 지운 뒤, 서버 렌더 완료 화면에 `다른 서비스에는 계속 로그인되어 있습니다.`와 `[모든 서비스에서 로그아웃]` 링크를 보인다.
   - `"모든 서비스에서 로그아웃"` = `POST {base_path}/api/auth/oidc/end-session`(**POST다** — 상태를 바꾸는 GET을 두지 않는다, arch-backend.md:3034).
     - 세션 행을 먼저 지우고, 그 행의 `id_token`을 `id_token_hint`로 실어 `end_session_endpoint`로 302한다.
     - ID 토큰은 만료돼도 된다(uni-auth/docs/manual/integration-guide.md:799). auth.db에 있으므로 재기동 뒤에도 버튼이 동작한다.
     - `post_logout_redirect_url`이 설정돼 있을 때만 `post_logout_redirect_uri`를 붙인다. 없으면 생략하고, 인증 서버 완료 화면에서 끝난다(uni-auth/docs/manual/integration-guide.md:796).
     - 이 버튼은 디스커버리에 `end_session_endpoint`가 있으면 늘 보인다(uni-auth/docs/manual/integration-guide.md:742).
6. **관리자 쓰기 전 재인증** (질문 Q5)
   - 관리자 쓰기(c.5)는 세션 행의 `reauth_at`이 N분(예: 15분, 추정값) 안일 때만 통과한다. 아니면 `403 forbidden` + `detail.reason: "reauth_required"`.
   - 클라이언트는 `start?reauth=1&return=…`로 top-level 이동하고, 서버는 `prompt=login`을 붙인다.
   - **오류 없이 돌아오고, 돌아온 `sub`가 세션의 `sub`와 같을 때만** `reauth_at`을 기록한다(uni-auth/docs/manual/integration-guide.md:684-685). `prompt=none` 재인가는 `reauth_at`을 바꾸지 않는다.
   - `auth_time`·`max_age`에는 기대지 않는다. `auth_time`은 uni-auth 클레임 표에 없는 값이다(uni-auth/docs/manual/integration-guide.md:376).
   - 오류로 돌아오거나 콜백이 오지 않으면 보류 중이던 동작은 버린다. 사용자가 다시 누른다.
7. **CSRF**: oidc 모드에서는 상태를 바꾸는 모든 요청(POST·PUT·DELETE)에 `Origin`(없으면 `Sec-Fetch-Site`)이 같은 출처인지 검사한다. 근거는 둘이다. uni-auth가 "`SameSite` + Origin 검증"을 요구하고(uni-auth/docs/manual/integration-guide.md:622), 같은 등록 도메인 아래의 이웃 서비스는 SameSite=Lax로 막을 수 없다.

### c.3 신원

- 사용자 키는 **AT의 `sub`**다(불투명 문자열, 파싱하지 않음). `email`은 키로 쓰지 않는다.
- 표시 이름은 `profile` scope의 `/userinfo` `name`(있을 때만 온다)에서 가져올 수 있다. 최소안은 표시 이름 없이 `sub` 앞 8자만 쓰고 `scope=openid`만 요청한다(질문 Q7).
- `tid == "default"`를 검증한다.

### c.4 접근 허용 (가장 중요한 결정)

uni-auth는 누구나 가입할 수 있는 B2C 시스템이다(b.1). "uni-auth에 로그인했다"를 "이 서재를 볼 수 있다"로 치면 가입한 아무나 컬렉션 전체를 본다. 세 안 중 추천은 (P1)이다(질문 Q2).

- **(P1) 설정의 허용 목록** `auth.oidc.allowed_subs: [...]`. 목록에 없는 `sub`는 403. 기본값은 거부다(빈 목록이면 아무도 못 들어온다). 매 요청 판정한다(c.2-3).
- (P2) uni-auth 서비스 role `<client_id>:reader`. 문서상 정석이지만 **부여 API가 아직 없다**(uni-auth/docs/manual/integration-guide.md:152). 지금은 쓸 수 없다.
- (P3) 전역 `platform_admin`만 허용. 가족에게 플랫폼 관리자를 줘야 하므로 맞지 않다.
- 기동 검증: `admin_subs`는 `allowed_subs`의 부분집합이어야 한다. 아니면 exit 2. Q4 (나)를 고르더라도 `platform_admin`인 사람이 `allowed_subs`에 있어야 한다.
- 사용자에게 알릴 것: 가족 구성원은 각자 uni-auth에 가입해야 한다. 가입은 이메일 검증까지 끝나야 완료된다(uni-auth/docs/manual/user-flows.md:101).

### c.5 인가: 누가 무엇을 쓰는가

허용된 사용자는 읽기와 **자기 데이터 쓰기**를 한다. 서버·컬렉션 전체에 영향을 주는 쓰기는 관리자만 한다. 관리자는 설정 `auth.oidc.admin_subs: [...]`로 정하고 매 요청 판정한다. `platform_admin`을 관리자로 인정할지는 질문 Q4다.

| 끝점 (router.go 기준) | 일반 사용자 | 관리자 |
| --- | --- | --- |
| `GET` 목록·상세·페이지·썸네일·표지·`/api/continue`·`/api/scan/status` | ✅ | ✅ |
| `PUT/DELETE /api/books/{bid}/progress`, `GET/PUT /api/books/{bid}/prefs` | ✅ 자기 데이터 | ✅ 자기 데이터 |
| `GET /api/progress/export` | ✅ 자기 데이터 + 공유 표지·방향(읽기) | ✅ |
| `POST /api/progress/import` | ✅ 자기 데이터. 공유 `covers`·`series_prefs` 절의 처리는 Q8을 따른다(c.8) | ✅ |
| `PUT /api/settings` 중 reader 기본값 | ✅ 자기 값 | ✅ |
| `PUT /api/series/{sid}/prefs`, `PUT/DELETE /api/books/{bid}/cover` (E-69) | 질문 Q8 | ✅ |
| `POST /api/scan`, `POST /api/scan/cancel`, `POST /api/series/{sid}/rescan` | ❌ 403 | ✅ 재인증 |
| `DELETE /api/cache` | ❌ 403 | ✅ 재인증 |
| `POST /api/roots`, `DELETE /api/roots/{name}`(A-11), `GET /api/browse`(A-12, 서버 디렉터리를 보여 준다) | ❌ 403 | ✅ 재인증 (기존 `allow_root_editing` 관문도 그대로) |
| `GET /api/scan/log`, `GET /api/settings`의 `server.config_path`(절대 경로) | 질문 Q8(추천: 관리자만) | ✅ |

403은 기존 `forbidden`(arch-backend.md:1781, A-11)을 재사용한다. `detail.reason`에 `"not_allowed"`(허용 목록 밖)·`"admin_only"`·`"reauth_required"`를 더한다. 401은 지금 뜻(세션 없음·만료, arch-backend.md:1780) 그대로다.

### c.6 데이터 분리와 user.db v4 가안, auth.db

**user.db** — 이전 규칙은 "새 단계는 `CREATE TABLE`/`CREATE INDEX IF NOT EXISTS`만"이다(arch-backend.md:917-918). 그래서 기존 테이블을 고치지 않고 **사용자별 테이블을 새로 더한다**(schema_version `'3'` → `'4'`).

| 기존 테이블 (arch-backend.md) | 분리 | v4 가안 |
| --- | --- | --- |
| `progress` (:730, FR-STT-001) | 사용자별 | `user_progress(user_sub, book_id, …기존 열…, PRIMARY KEY(user_sub, book_id))` + 이어보기 인덱스 |
| `book_prefs` (:764, FR-VWR-002) | 사용자별 | `user_book_prefs(user_sub, book_id, …)` |
| `settings` (:772, reader 기본값) | 사용자별 | `user_settings(user_sub, key, value)` |
| `view_state` (:778) | 사용자별 | `user_view_state(user_sub, key, value)` |
| `series_seen` (:789, 최근 추가) | 공유 | 그대로 — 컬렉션의 사실이다 |
| `book_covers` (:803, E-69) | 추천 공유 | 그대로(질문 Q8) |
| `series_prefs` (:821, E-69 시리즈 방향) | 추천 공유 | 그대로(질문 Q8) |
| `pending_relocations` (:830) | 공유 | 그대로. 다만 권 ID 이동 처리(E-69 §2.13)가 새 사용자별 테이블도 옮겨야 한다 |
| (새) | — | `users(user_sub PRIMARY KEY, first_seen_at, last_login_at)` — 이름·이메일은 저장하지 않는다 |

- 단일 사용자 모드(인증 없음·비밀번호)는 지금 테이블을 그대로 쓰고, oidc 모드만 `user_*` 테이블을 쓴다. 저장소 계층이 사용자 키를 받는 인터페이스로 바뀐다(c.12).
- 옛 빌드는 `'4'` 파일을 `ErrSchemaTooNew`로 열지 않는다(arch-backend.md:918-919 취지). 되돌리려면 백업이 필요하다.

**auth.db (새 파일)** — `<data_dir>/auth.db`에 `oidc_sessions`(c.2-3)를 둔다. user.db와 분리하는 이유는 둘이다.
- user.db는 "잃으면 안 되는 사용자 데이터"이고(arch-backend.md:713) 세션은 그렇지 않다.
- auth.db는 지워도 모두 로그아웃될 뿐이라 안전한 방향으로만 실패한다.

check-readonly의 두 DB 규칙(D-13)과 같은 모양으로, auth.db와 user.db에 걸친 트랜잭션은 두지 않는다.

### c.7 설정 가안 (arch §3.2 `auth:` 블록, 지금 arch-backend.md:419-425)

```yaml
auth:
  mode: "oidc"                 # 새 키. "password"(현행) | "oidc". 블록이 없으면 지금처럼 인증 없음
  session_ttl: "12h"           # 기존 키. oidc 모드 권장값(추정)
  session_key_file: ""         # 기존 키. password 모드만 쓴다
  oidc:
    issuer: "https://auth.betterax.net"          # 디스커버리 = issuer + /.well-known/openid-configuration
    client_id: "zip-viewer"                      # uni-auth에 등록한 값
    audience: "https://<zip-viewer 호스트>/api"   # 등록한 audiences 중 하나 (AT aud 검증)
    redirect_url: "https://<zip-viewer 호스트>{base_path}/api/auth/oidc/callback"   # Host 헤더로 만들지 않는다
    post_logout_redirect_url: ""                 # 선택. 비우면 복귀 주소 없이 통합 로그아웃
    allowed_subs: []                             # 비어 있으면 아무도 못 들어온다
    admin_subs: []                               # allowed_subs의 부분집합
    legacy_owner_sub: ""                         # c.9 이관 대상
```

- 기동 실패(exit 2) 조건:
  - `mode: oidc`인데 `issuer`·`client_id`·`audience`·`redirect_url` 중 하나가 비었을 때.
  - `redirect_url`이 https가 아닐 때(uni-auth/docs/manual/integration-guide.md:164 R3).
  - `password`·`password_hash`와 `oidc`를 함께 줬을 때.
  - `admin_subs`가 `allowed_subs` 밖일 때.
- **기존 단일 비밀번호와의 관계는 설정으로 고르는 배타 선택**을 추천한다(질문 Q1). 병행하면 로그인 경로가 둘이 되고 비밀번호 사용자에게 줄 `sub`가 없다. uni-auth 장애 때의 비상 경로는 "운영자가 `mode: password`로 바꿔 재시작"이다. 쿠키를 모드마다 나눴으므로(c.1) 이때 oidc 세션은 통과하지 못한다.
- **https 호스트가 필요하다.** uni-auth는 `dev_mode=false` 클라이언트에 `https://` 콜백만 받는다(uni-auth/docs/manual/integration-guide.md:164). 공개 도달은 요구하지 않는다(브라우저가 리다이렉트를 따라갈 뿐). zip-viewer는 TLS를 내장하지 않으므로(impl-plan.md:282 Built-in TLS OUT) 앞에 TLS 종단(리버스 프록시나 터널)이 있어야 한다. 선택지는 질문 Q9.
  - (가) 공개 터널(예: cloudflared). 컬렉션이 인터넷에 노출되고, 페이지 바이트가 외부 경유지를 지난다. NFR-PRF-001 "로컬 네트워크 기준 100ms"(prd.md:188)를 다시 재야 한다.
  - (나) LAN 전용 https: 사내 DNS 이름 + 인증서(예: DNS-01로 발급). 컬렉션은 LAN 밖에 노출되지 않는다. 로그인 때 브라우저와 zip-viewer 서버는 여전히 `auth.betterax.net`에 닿아야 한다.
  - 어느 쪽이든 oidc 모드에서 `http://NAS:포트`로 직접 쓰는 길은 없다(`Secure` 쿠키, https 콜백).
- `server.trusted_proxy_headers`(arch-backend.md:305)는 oidc 모드에 필요하지 않다(`Secure` 고정, `redirect_url` 고정). 지금 키는 출처 제한 없이 `X-Forwarded-For`를 믿으므로 켜면 IP별 제한을 위조로 우회할 수 있다. 끄는 것을 권장하고, 꼭 필요하면 신뢰 프록시 CIDR 키를 따로 둔다(uni-auth 자신도 신뢰 프록시를 `172.21.0.1/32` 하나로 제한한다, uni-auth/docs/manual/install-guide.md:251).

### c.8 내보내기·가져오기 (FR-STT-004, arch §7.11)

- oidc 모드에서는 **호출한 사용자 자신의 데이터만** 내보내고 가져온다. 형식은 버전을 하나 올리고, 사용자 키는 파일에 넣지 않는다(다른 사용자에게 옮겨 넣을 수 있게).
- 지금 형식은 공유 테이블인 `covers`·`series_prefs`도 싣고, `?strategy=replace`면 덮어쓴다(arch-backend.md:2924, :2937, :2959-2960). oidc 모드에서 이 두 절의 처리는 Q8을 따른다.
  - Q8 (가): 누구나 쓸 수 있다 → 지금처럼 병합한다.
  - Q8 (나): 관리자만 → 일반 사용자의 가져오기는 두 절을 건너뛰고, 응답의 건수에 "건너뜀"으로 적는다.
- 관리자의 전체 내보내기는 최소안에 넣지 않는다.

### c.9 기존 단일 사용자 데이터 이관과 되돌리기

- `auth.oidc.legacy_owner_sub`에 적힌 `sub`가 **처음 로그인할 때 1회** 기존 `progress`·`book_prefs`·`settings`·`view_state`를 `user_*`로 복사한다.
  - 복사 완료는 `meta.legacy_migrated_to = <sub>`로 기록한다(재실행 방지).
  - 기존 테이블은 지우지 않는다.
- `legacy_owner_sub`가 비어 있으면 이관하지 않는다.
- 되돌리기(oidc → password): 복사는 한 방향이라, 되돌리면 oidc 모드에서 쌓인 소유자 진행률이 보이지 않는다. 같은 이관 코드를 반대로 도는 일회성 명령(`shelf auth restore-owner`, 이름은 가안)을 둘지는 질문 Q12.

### c.10 런타임 의존성과 uni-auth 장애 시 동작

- **새 런타임 의존:**
  - zip-viewer 서버가 `https://auth.betterax.net`에 나가는 연결(디스커버리·JWKS·`/token`).
  - 로그인 때 브라우저가 그 호스트에 닿는 연결.
  - 이 의존은 NFR-OPS-002 "런타임 외부 의존성은 설정 파일과 캐시 디렉터리 외에 없어야 한다"(prd.md:200)와 핵심 가치 5 "런타임 의존은 설정 파일과 캐시 디렉터리뿐"(intent.md:21)에 **직접 충돌**한다(d절). `mode: oidc`를 켠 설치에만 해당한다.
- 장애 시 동작:
  - 이미 만든 세션은 zip-viewer가 auth.db로 스스로 확인하므로 **계속 동작한다**. (S2)는 세션이 만료될 때, (S3)는 15분 갱신 때 실패한다.
  - 새 로그인은 실패한다. 로그인 화면은 "인증 서버에 연결할 수 없습니다"와 재시도를 보여 준다.
  - 기동 시 디스커버리를 받지 못해도 서버는 뜬다(AC-007, prd.md:347). 디스커버리는 첫 로그인 때 다시 시도한다.
  - JWKS·디스커버리 캐시는 메모리에만 둔다(30분·1시간, uni-auth 권장값).
  - `/api/health`는 uni-auth 상태와 무관하게 200이다.
- **계정 삭제:** uni-auth는 `user.deleted` 이벤트를 webhook으로 보낼 수 있고(uni-auth/docs/manual/integration-guide.md:952-969), 차단 이벤트는 없다(uni-auth/docs/manual/integration-guide.md:503). 처리 방식은 질문 Q11.

### c.11 HTTP 계약 개정 가안 A-20 (가번호)

| 대상 | 바뀌는 것 |
| --- | --- |
| `GET /api/auth/status` (arch-backend.md:2971) | 응답에 `mode: "none" \| "password" \| "oidc"`, `user?: {sub: string, is_admin: boolean}`, `end_session_available: boolean`을 더한다. 기존 두 필드는 그대로 |
| `POST /api/auth/login` (arch-backend.md:2972) | oidc 모드에서는 `400 bad_request` + `detail.reason: "oidc_mode"`로 거절한다. 경로를 지우면 405·404가 되어 "기능 꺼짐"과 "옛 서버"를 구별할 수 없다 — router.go가 A-11 경로에 쓴 것과 같은 이유다 |
| `GET /api/auth/oidc/start`, `GET /api/auth/oidc/callback` | **새 끝점.** 302 또는 서버 렌더 HTML. `/api/auth/*` 인증 예외(arch-backend.md:3035)에 들어간다 |
| `POST /api/auth/oidc/end-session` | **새 끝점.** 같은 출처 검사(c.2-7) 뒤 세션 삭제 → 302 |
| `POST /api/auth/logout` | 그대로. oidc 모드에서는 세션 행도 지운다 |
| 쿠키 | oidc 모드: `shelf_sid`(세션 ID), `shelf_login`(사전 인증, 10분). password 모드: `shelf_session` 그대로 |
| 에러 | `403 forbidden`(arch-backend.md:1781)의 `detail.reason`에 `not_allowed`·`admin_only`·`reauth_required`를 더한다. 같은 출처 검사 실패도 `403`(`detail.reason: "cross_origin"`). `401 unauthorized`의 뜻은 그대로 |
| `GET /api/progress/export`·`POST /api/progress/import` (arch §7.11, arch-backend.md:2900) | 사용자 범위와 공유 절 처리(c.8), 형식 버전 |
| `Settings` (arch-backend.md:2750 부근) | `auth_mode` 필드를 더한다. `server.config_path`는 관리자에게만 채운다 |
| impl-plan §0.3 | A-20 행을 먼저 넣는다(contractcheck가 arch의 `AMENDMENT A-n`과 대조한다, scripts/contractcheck/main.go:1075-1079) |

### c.12 영향

- **코드:**
  - `internal/auth/auth.go`(`Options` 125행, `Authenticator` 156행, `Login` 334행, `Cookie` 362행)·`internal/auth/session.go`를 모드별로 나눈다.
  - 새 패키지 `internal/auth/oidc`(추정)를 둔다. 담당: 디스커버리·JWKS 캐시, EdDSA JWT 검증, PKCE, 사전 인증 항목, auth.db 세션 저장소.
  - `internal/httpapi/authhandlers.go`(`handleLogin` 49행), `internal/httpapi/loginpage.go`(로그인 버튼·403·로그아웃 완료 화면), `internal/httpapi/router.go` 경로 표(305~370행 부근)와 관리자·재인증·같은 출처 관문, 요청 로그의 쿼리 제거.
  - `internal/userdata/schema.go`(v4 단계), 진행률·prefs·settings·view_state 저장소 전부(사용자 키 인자).
  - `web/src/features/auth/`(401 → top-level 재인가, 로그아웃 버튼 2종).
- **의존성:** 새 Go 모듈 없이 표준 라이브러리(`crypto/ed25519`, `crypto/sha256`, `encoding/json`, `net/http` 클라이언트)로 구현하는 것을 추천한다. EdDSA 하나만 허용하므로 범용 JOSE 라이브러리가 필요 없다(추정). 라이브러리를 쓰면 D-08 고정 목록(decisions.md:32)과 intent 승인 항목(intent.md:30)에 걸린다. auth.db는 이미 있는 `modernc.org/sqlite`를 쓴다. CGO 없음.
- **크기:** TLS 클라이언트 코드는 `net/http`가 이미 끌어온다. 증가는 수십 KB 수준으로 본다(추정, 미측정). 여유는 343 902 B다(Makefile:115·138). `scratch` Docker 이미지라면 CA 인증서가 필요하다(arch-backend.md:3300이 이미 적어 둠).
- **검사:**
  - 단위·통합: 가짜 OIDC 공급자(테스트용 EdDSA 키로 토큰 발급)를 `httptest`로 띄운다. 실제 uni-auth는 테스트에 쓰지 않는다.
  - 보안 회귀 테스트: login CSRF(다른 브라우저의 콜백), `return` 오픈 리다이렉트, 모드 전환 뒤 옛 쿠키, 허용 목록에서 뺀 `sub`, 교차 출처 POST, 사전 인증 항목 상한.
  - e2e: 합성 라운드에 가짜 공급자를 붙인 로그인·사용자 분리·403 시나리오를 넣는다.
  - contractcheck: auth status·settings·export 골든을 갱신한다.
  - check-readonly(scripts/check-readonly.sh): 새 DB 파일도 트랜잭션 분리 규칙에 넣는다.
- **되돌리기:** 설정을 `mode` 없음이나 `password`로 돌리면 기존 테이블이 그대로라 단일 사용자로 돌아간다. 다만 v4 파일은 옛 빌드가 열지 못하고(c.6), oidc 모드에서 쌓인 진행률은 역복사가 있어야 보인다(c.9). **중간 난이도.**

## d. 충돌·개정이 필요한 문서

**직접 충돌**(이 계약을 하면 문구가 틀려진다):

| 문서 | 위치 | 내용 |
| --- | --- | --- |
| prd | NFR-SEC-002 (prd.md:228) | "단일 사용자 비밀번호 인증" → 비밀번호 또는 외부 OIDC(uni-auth) 중 선택 |
| prd | NFR-OPS-002 (prd.md:200) | 런타임 외부 의존성 금지 → oidc 모드의 예외 명시 |
| intent | 핵심 가치 5 (intent.md:21) | "런타임 의존은 설정 파일과 캐시 디렉터리뿐" → oidc 모드 예외 |
| intent | 경계 (intent.md:40) | "하지 않는다: 다중 사용자 계정·권한" → uni-auth 위임 다중 사용자는 "한다"로 |
| intent | 경계 (intent.md:39) | "3차 로드맵 다섯 기능(판정 전)" → 다중 사용자가 빠져 넷이 된다 |
| arch | §7.12 (arch-backend.md:2968-2975), §8.2 (arch-backend.md:3028-3035) | 인증 계약, 세션(oidc 모드는 서버 측 세션). §8.2의 "no state-changing endpoint is a `GET`"(arch-backend.md:3034)에 OIDC 콜백(세션 생성)이라는 구조적 예외를 적는다 |
| arch | §3.6 (arch-backend.md:713~), §3.2 `auth:` (arch-backend.md:419-425) | v4 테이블, auth.db, 설정 키 |
| arch | §7.11 (arch-backend.md:2900), §7.2 (arch-backend.md:1780-1781), §7.13 (arch-backend.md:2977) | 내보내기 범위, `forbidden` reason, 끝점 표 |

**사용자 승인이 필요한 항목**(문구는 바뀌지 않는다): intent "사용자 승인"의 새 런타임 의존성 추가(intent.md:30). 이 판정을 확정하는 일이 곧 그 승인이다.

**판정에서 검토:**
- intent "쓰지 않을 사람: 여러 사용자에게 계정을 나눠 서비스하려는 운영자"(intent.md:13): 대상은 여전히 "본인 또는 소수 가족"(intent.md:11)이고 허용 목록이 그 크기를 지킨다. 문구는 유지하되 판정에 이 해석을 적는다.
- prd §7.1 "동시 접속자는 소수이며 쓰기 경합이 사실상 발생하지 않는다"(prd.md:320): 허용 목록이 소수를 유지하므로 가정은 선다. 사용자별 테이블이라 쓰기 경합도 늘지 않는다(추정).
- FR-STT-001(prd.md:175)·FR-VWR-002(prd.md:159)·FR-STT-004(prd.md:178): 문구가 사용자 수를 말하지 않으므로 충돌하지 않는다. 분리 방식(c.6·c.8)을 판정에 적는다.
- NFR-PRF-001 "로컬 네트워크 기준 100ms"(prd.md:188): Q9 (가) 공개 터널을 고르면 다시 잰다.
- AC-007(prd.md:347): 기동은 설정 파일만으로 된다(c.10). 로그인에는 uni-auth가 필요하다.
- AC-005·006(prd.md:345-346): 사용자별 진행률도 user.db에 있으므로 인덱스 재구축 뒤에 남는다. auth.db를 지우면 로그아웃될 뿐이다.
- impl-plan §1.3 OUT "multi-user" 행(impl-plan.md:284)은 IN으로 옮긴다.

## e. 사용자 결정 질문

| # | 질문 | 선택지 | 추천과 이유 |
| --- | --- | --- | --- |
| Q1 | 기존 단일 비밀번호와의 관계 | (가) 설정으로 배타 선택 (나) 병행 (다) 비밀번호 제거 | **(가)** — 로그인 경로가 하나라 보안 검토 범위가 작다. 비밀번호 모드를 남기면 uni-auth 장애 때 운영자가 설정 한 줄로 되돌릴 수 있다 |
| Q2 | 누가 들어올 수 있나 | (P1) 설정 `allowed_subs` (P2) uni-auth 서비스 role (P3) `platform_admin`만 | **(P1)** — uni-auth는 공개 가입 B2C라 로그인만으로 허용하면 가입자 누구나 서재를 본다. P2는 부여 API가 없어 지금 쓸 수 없다 |
| Q3 | 세션 만료·차단 반영 | (S1) TTL만 (S2) 짧은 TTL + `prompt=none` 재인가 (S3) refresh token 보관 | **(S2)** — 토큰 저장 없이 uni-auth 쪽 차단을 TTL(예 12h) 안에 반영한다. 허용 목록에서 빼는 차단은 매 요청 판정이라 TTL과 무관하다. S3는 회전·재사용 탐지 때문에 구현 위험이 크다 |
| Q4 | 관리자 판정 | (가) 설정 `admin_subs`만 (나) + 전역 `platform_admin` | **(가)** — 플랫폼 관리자가 서재 관리자일 이유가 없고, 설정만 보면 누가 관리자인지 알 수 있다 |
| Q5 | 관리자 쓰기 전 재인증 | 예 / 아니오 | **예** — uni-auth가 관리자 기능에 재확인을 요구한다(uni-auth/docs/manual/integration-guide.md:496-507). 스캔·루트 편집은 드물어 불편이 작다 |
| Q6 | oidc 모드의 세션 저장 | (가) 새 파일 auth.db의 서버 측 세션 (나) user.db 안의 테이블 (다) 지금처럼 무상태 HMAC 쿠키 | **(가)** — 즉시 차단·서버 측 로그아웃·ID 토큰 보관(통합 로그아웃 버튼, UI 계약)이 된다. 지워도 모두 로그아웃될 뿐이라 잃으면 안 되는 user.db와 분리한다. (다)는 uni-auth 계약(쿠키는 세션 ID만, 즉시 파기)을 지킬 수 없다 |
| Q7 | 표시 이름 | (가) `sub` 축약만, `scope=openid` (나) `profile` scope로 `name` | **(가)** — 이름·이메일을 받지 않으면 저장할 개인정보가 없다. 가족 몇 명이면 축약 ID로 충분하다(추정) |
| Q8 | 권 표지(E-69)·시리즈 방향·스캔 로그·설정 경로 | (가) 표지·방향은 공유+누구나, 로그·경로는 관리자만 (나) 표지·방향도 관리자만 (다) 표지·방향을 사용자별로 | **(가)** — 표지·방향은 컬렉션의 성질이고 가족이 함께 고친다. 서버 경로는 관리 정보다. 가져오기(c.8)도 이 답을 따른다 |
| Q9 | zip-viewer를 어디에 노출하나 | (가) 공개 터널(인터넷 노출) (나) LAN 전용 https(사내 DNS·인증서) | **(나)** — 컬렉션을 인터넷에 내놓지 않고 페이지 바이트가 LAN에 머문다(NFR-PRF-001). 인증서 발급 수단이 없으면 (가) |
| Q10 | uni-auth 클라이언트 등록 | 사용자가 직접 등록 / 등록 작업을 별도 승인 작업으로 배정 | **사용자 직접 또는 별도 승인 작업** — uni-auth에 쓰는 일이라 이 프로젝트 세션의 범위 밖이다. 지금 실제 등록 경로는 Hydra admin `POST /admin/clients` 1회이고 관리 API·테이블은 아직 없다(uni-auth/docs/manual/operations-guide.md:603-607, :619). 등록 항목: `client_id`·`name`·`redirect_uris`(콜백)·`post_logout_redirect_uris`(쓸 때)·`audiences`·`scopes` |
| Q11 | uni-auth 계정 삭제 시 | (가) 운영자가 `allowed_subs`에서 빼고 데이터는 남긴다 (나) webhook `user.deleted`를 받아 세션·데이터를 지운다 | **(가)** — webhook은 수신 끝점·서명 검증이 새로 필요하고 P1 기능이다. 허용 목록 판정이 매 요청이라 빼는 즉시 접근이 끊긴다 |
| Q12 | oidc → password로 되돌릴 때 | (가) 역복사 없음(문서로 알림) (나) 소유자 데이터를 기존 테이블로 되돌리는 일회성 명령 | **(나)** — 이관 코드의 반대 방향이라 작고, 되돌리기 비용을 "백업 복원"에서 "명령 하나"로 낮춘다 |

## 확정 뒤 순서 (제안)

1. 사용자가 Q1~Q12에 답한다.
2. intent.md(핵심 가치 5, 경계 두 칸)를 사용자 승인으로 고친다.
3. `docs/decisions.md`에 E-74(번호 재확인)를, impl-plan §0.3에 A-20 행을 쓰고, arch §3.2·§3.6·§7.2·§7.11·§7.12·§7.13·§8.2를 고친다.
4. uni-auth 클라이언트 등록(Q10)과 https 호스트(Q9)를 준비한다.
5. 구현을 배정한다. 가짜 OIDC 공급자와 보안 회귀 테스트를 먼저 만든다.

## 리뷰

독립 리뷰 1회(서브에이전트, opus, effort high, 읽기 전용·네트워크 없음, 2026-11-08). 근거로는 이 초안, intent.md, prd.md, arch §7.12·§8.2, uni-auth 연동 문서의 6fc19d2 사본만 줬다. 지적 20건을 모두 수용했다. 리뷰가 든 근거는 이 세션에서 직접 열어 확인했다. uni-auth/docs/manual/integration-guide.md:669·677·684-685·801·827·911·938·959, uni-auth/docs/manual/operations-guide.md:603·606-607·619, uni-auth/docs/manual/install-guide.md:251, arch-backend.md:2924·2937·2959-2960, internal/httpapi/loginpage.go가 그것이다. [2]에는 사실 하나를 더했다: 지금 코드는 `v`가 1이 아닌 세션 토큰을 이미 거부한다(internal/auth/session.go:80). 그래서 쿠키 혼동은 두 모드를 함께 아는 새 빌드에서 생기는 위험이다. 쿠키를 모드별로 나눠 막았다.

| # | 심각도 | 지적 | 처리 |
| --- | --- | --- | --- |
| 1 | 높음 | state가 시작한 브라우저에 묶이지 않아 login CSRF가 된다 | 수용. 사전 인증 쿠키 `shelf_login`을 두고 콜백에서 state와 함께 대조한다(c.2-1·2) |
| 2 | 높음 | 모드를 전환하면 같은 HMAC 키의 쿠키가 다른 모드에서 통과한다 | 수용. oidc 모드는 `shelf_sid`(서버 측 세션), password 모드는 `shelf_session`만 읽는다(c.1) |
| 3 | 높음 | 인가를 로그인 때 한 번만 판정하고 `adm`을 쿠키에 고정해 즉시 차단이 안 된다 | 수용. auth.db 서버 측 세션을 두고, 허용·관리자 여부는 매 요청 설정에서 판정한다(c.1·c.2-3·c.5, Q6 신설) |
| 4 | 중간 | 가져오기로 공유 `covers`·`series_prefs`를 덮어써 Q8을 우회한다 | 수용. c.8에 Q8에 따른 처리를 적었다 |
| 5 | 중간 | 재인증 시각의 출처가 없다(`auth_time`은 계약 밖이다) | 수용. `prompt=login` 콜백이 오류 없이 돌아오고 `sub`가 같을 때만 `reauth_at`을 기록한다(c.2-6) |
| 6 | 중간 | 같은 사이트의 이웃 서비스가 보내는 CSRF를 SameSite만으로는 못 막는다 | 수용. oidc 모드의 상태 변경 요청에 같은 출처 검사를 넣는다(c.2-7, A-20) |
| 7 | 중간 | 로그아웃 UI 계약 위반(버튼 숨김), ID 토큰 보관, 등록 항목 누락 | 수용. 버튼은 늘 보이고, ID 토큰은 auth.db에 두고, 복귀 주소는 선택이다. Q10에 `post_logout_redirect_uris`를 넣었다 |
| 8 | 중간 | 인증 없는 `/start`로 메모리를 고갈시킬 수 있다 | 수용. 항목 상한, IP별 제한, 만료 정리를 넣었다(c.2-1) |
| 9 | 중간 | "공개 HTTPS"는 과잉 요구다. LAN 전용 https 선택지가 빠졌고, Q9의 루프백 선택지는 기동 검증과 모순된다 | 수용. Q9를 "인터넷 노출 여부"로 바꾸고 루프백을 뺐다. NFR-PRF-001을 검토 항목에 넣었다 |
| 10 | 중간 | (S2)에 `prompt=none` 폴백·루프 방지 규칙이 없다 | 수용. c.2-4에 옮겼다 |
| 11 | 중간 | 상태를 바꾸는 GET(end-session)이 생긴다. 콜백 예외도 적지 않았다 | 수용. end-session을 POST로 바꾸고, d절에 §8.2 문장의 콜백 예외를 적었다 |
| 12 | 낮음 | `trusted_proxy_headers`를 켤 근거가 없고 켜면 IP를 위조할 수 있다 | 수용. 끄는 것을 권장하고 필요하면 CIDR 키를 둔다(c.7) |
| 13 | 낮음 | `return` 보관·검증, `redirect_uri` 동일성, 콜백 중복, `sub` 일치, issuer 대조, 로그 마스킹, NTP | 수용. 모두 c.2에 넣었다 |
| 14 | 낮음 | back-channel 인용 행 오류(:800) | 수용. :801로 고쳤다 |
| 15 | 낮음 | 등록 경로가 운영 가이드의 현재 사실(관리 API 없음, Hydra admin 1회)과 다르다 | 수용. b.1 행과 Q10을 고쳤다 |
| 16 | 낮음 | 계정 삭제·webhook 결정이 빠졌다 | 수용. c.10에 적고 Q11을 신설했다 |
| 17 | 낮음 | 관리자 목록과 허용 목록의 관계가 정해지지 않았다 | 수용. 부분집합 기동 검증을 넣었다(c.4·c.7) |
| 18 | 낮음 | 역이관과 가족의 가입 부담 | 수용. c.9와 Q12를 신설하고 c.4에 가입 안내를 넣었다 |
| 19 | 낮음 | 세션 없이 보이는 화면을 어디서 그리는가 | 수용. 서버 렌더 HTML(loginpage.go 방식)로 정했다(c.2 머리) |
| 20 | 낮음 | d절 분류: intent.md:30은 과장, intent.md:39는 누락, NFR-PRF-001 누락 | 수용. :30은 "사용자 승인 필요"로 옮기고, :39는 직접 충돌에, NFR-PRF-001은 검토에 넣었다 |

반박한 지적은 없다. 2사이클 리뷰는 돌리지 않았다(배정은 1회). 고친 뒤의 계약(서버 측 세션·사전 인증 쿠키)은 리뷰를 다시 받지 않은 상태다.
