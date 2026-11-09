# 작업 계획

갱신: 2026-11-10T00:4x (Orchestrator t7c, 시작 계약 5 추가)

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

## 시작 계약 5 (books 대화형 설치·운영 마법사, 2026-11-10)

사용자 구체화(Commander 경유 00:4x, 아래 마법사 지시에 우선): "그냥 scripts/start.sh 하면 zip-viewer가 잘 동작하게. 운영 진입점을 정확히 scripts/start.sh로, 첫 실행 시 books/uni-auth 설정과 Cloudflare 연결을 대화형으로 안내·처리, 이후 실행은 간단히. 불필요한 단계 제거, 데이터 보존·인증 검사·sudo 표준 프롬프트·기존 서버 종료 동의 유지. 기존 구현 재사용, 실행·재실행·실패 경로 검증." → 완료 조건 1의 "한 명령"은 scripts/start.sh, 이후 실행은 질문 없이 점검·기동·상태 요약, 완료 조건 6에 이후 실행·이미 실행 중·비대화형 첫 실행 거부를 더한다.

요청(사용자, Commander t1h 경유 00:4x): "현재 운영 적용 절차가 너무 번거롭다. 한 명령으로 설정 생성, 안전 점검, 기존 서버 처리 안내, 서버 시작, Cloudflare 적용, 첫 로그인 후 sub 입력과 설정 반영·재시작까지 안내하는 대화형 설치/운영 마법사. 사용자가 명시적으로 실행하는 흐름에서만 변경, 관리자 비밀번호는 표준 sudo 프롬프트, 기존 설정·데이터 백업, 인증 없는 공개 방지, 실패 시 복구 유지. 기존 서버 자동 종료는 대상 확인과 명시적 대화형 동의, 프로젝트 서버 재시작은 사용자가 스크립트를 직접 실행할 때만. 검증·문서화, 보기 패널 유지."
시작 계약 4(books.sh, main 0bd88f7)를 바탕으로 한다. 계약 4의 "설정 파일을 쓰지 않는다"는 이 요청으로 마법사 안 sub 반영 단계에 한해 바뀐다(대화형 동의·백업·검증 조건).

- 완료 조건:
  1. 한 명령(books.sh의 하위 명령 또는 같은 디렉터리의 진입점)이 TTY 대화형으로 설정 생성(없을 때만) → 안전 점검 → 기존 서버 처리 → 기동 → ingress 적용 → cloudflared 재시작 → 첫 로그인 안내 → sub 입력·설정 반영 → 재시작 → 상태 요약까지 이어 간다. 각 단계 전에 할 일과 영향을 보여 주고 동의를 받으며, 이미 끝난 단계는 감지해 건너뛰고 중간에 멈춘 뒤 다시 실행하면 이어 간다.
  2. 인증 없는 공개 방지·실패 복구는 계약 4의 관문을 그대로 쓴다(oidc·루프백·books 등록값·바이너리 설정 검사, 로컬 verify FAIL 0일 때만 ingress, 공개 verify FAIL이면 롤백). 마법사가 관문을 우회하는 경로가 없다.
  3. 기존 서버: 같은 data_dir을 쓰거나 대상 포트를 쥔 shelf를 찾아 pid·명령줄·cwd·설정·포트를 보여 주고, 대화형으로 명시 동의(예: 대상 pid를 직접 입력)를 받은 경우에만 TERM한다. 비대화형(파이프·--yes)으로는 끄지 않는다. 끈 뒤 되살리는 명령을 출력한다.
  4. sudo: 스크립트가 비밀번호를 읽거나 저장하지 않고 표준 sudo 프롬프트에 맡긴다. sudo 단계는 따로 동의를 받는다.
  5. 설정·데이터 보존: 기존 설정은 덮어쓰지 않는다. sub 반영은 대화형 동의 뒤에만, 반영 전 설정 백업·변경 내용(diff) 표시, sub 형식 검증, 바이너리 설정 검사 통과 뒤 재시작, 실패하면 설정을 백업으로 되돌리고 기존 인스턴스를 유지(또는 직전 정상 설정으로 복구). 데이터 백업은 계약 4와 같다. allow_root_editing 등 공개 전에 정할 값은 마법사가 현재 값과 영향을 보여 주고 사용자가 고르게 한다.
  6. 자동 테스트가 의사 터미널(pty)로 대화 흐름 전체·중단 후 재실행·동의 거부·비대화형 거부·기존 서버 종료 동의/거부·sub 반영 실패 복구를 임시 포트·설정·data_dir·가짜 sudo/systemctl/cloudflared로 실행한다. 운영 경로 비접촉. 사용법은 새 런북과 --help. make lint·make test PASS, 독립 검증 PASS 뒤 병합.
- 허용 범위: scripts/deploy/library/**, 테스트, docs/progress 새 런북. 읽기 전용 check·status를 현재 환경에 실행 허용. 금지: 운영 경로에서 마법사·start·stop·restart·tunnel 실행, :9909·:9910 조작, sudo·/etc·systemctl, shelf.yaml·shelf.books.yaml 생성·수정, uni-auth·DNS 쓰기, push.
- 탈출 조건: shelf 바이너리 변경이 필요할 때, 사용자 환경 쓰기 없이 검증할 수 없을 때 → [차단].
- 묶음 U-R5: worker-3(U-R4 맥락, 같은 파일).

## 시작 계약 4 (books 운영 실행 스크립트, 2026-11-09)

요청(사용자, Commander t1h 경유 21:4x): "상용 실행 스크립트. books.betterax.net + uni-auth 운영을 기존 수동 절차(§5 A~F)보다 쉽게 시작·재시작·상태 확인하도록 실행 스크립트와 사용법을 구현·검증. 기존 데이터 보존, 인증 없는 공개 방지, Cloudflare 변경과 sudo·기존 서비스 영향은 명시적 단계로, 실패 시 안전한 중단과 복구. 사용자 설정값은 검증하고 임의로 덮어쓰지 않는다. 운영 서버 재시작은 사용자가 직접, 현재 실행 환경은 건드리지 않는다. 독립 검증·보이는 패널 유지."
읽기 확인(21:4x): :9909·:9910 listen 없음, dist/shelf 프로세스 없음, shelf.books.yaml 없음(shelf.yaml만), uni-auth 127.0.0.1:8443 listen.

- 완료 조건:
  1. 한 진입점 스크립트(scripts/deploy/library/ 아래)가 check·status·start·stop·restart·tunnel(적용·재시작)·rollback·verify를 제공하고 `--help`에 각 단계의 영향(sudo 여부, 끊기는 기존 서비스)을 적는다. 기존 preflight·tunnel-apply·tunnel-rollback·verify-public을 재사용한다.
  2. 인증 없는 공개 방지: auth.mode≠oidc, listen이 루프백이 아님, issuer·client_id·audience·redirect가 books 값과 다름 중 하나면 start·restart·tunnel이 아무것도 바꾸지 않고 0이 아닌 코드로 멈춘다. tunnel은 로컬 verify-public FAIL 0일 때만 진행한다.
  3. 데이터·설정 보존: 스크립트는 사용자 설정 파일을 쓰지 않는다(초기 생성은 파일이 없을 때만, 있으면 거부). start 전 data_dir 백업, 백업·데이터 삭제 없음. 같은 data_dir을 쓰는 다른 shelf(:9909 등)가 돌면 start 거부.
  4. 실패 시 안전 중단·복구: 기동 뒤 헬스·로컬 verify FAIL이면 새 인스턴스를 멈추고 복구 명령을 출력, 공개 verify FAIL이면 ingress 백업으로 되돌린다. stop은 pidfile·포트·명령줄이 모두 맞는 프로세스만 끈다. 변경 단계는 확인(대화형 또는 명시 플래그)이 필요하고 sudo 단계는 따로 확인한다. --dry-run은 아무것도 바꾸지 않는다.
  5. 자동 테스트가 임시 포트·임시 설정·임시 data_dir·가짜 cloudflared/systemctl/sudo로 위 2~4를 실행해 보이고, :9909·:9910·/etc·실제 cloudflared·uni-auth를 건드리지 않는다. 사용법은 새 런북(docs/progress)과 --help. make lint PASS, 독립 검증 PASS 뒤 병합.
- 허용 범위: scripts/deploy/library/**, 테스트, docs/progress 새 런북. 읽기 전용 status·check를 현재 환경에 실행하는 것은 허용. 금지: 실제 start·stop·restart·tunnel 실행, :9909·:9910 조작, sudo·/etc·systemctl, shelf.yaml·shelf.books.yaml 생성·수정, uni-auth·DNS 쓰기, push.
- 탈출 조건: 안전 조건을 지키려면 shelf 바이너리 변경이 필요할 때, 사용자 환경 쓰기 없이 검증할 수 없을 때 → [차단].
- 묶음 U-R4: worker-3(U-R3a·b 맥락, 같은 파일). U-R3c와 독립(사용자 적용은 이 스크립트로 해도 되고 §5로 해도 된다).

## 시작 계약 3 (운영 주소 books.betterax.net, 2026-11-09)

요청(사용자, Commander t1h 경유 17:3x): "운영 주소 library.betterax.net을 https://books.betterax.net으로 변경, Cloudflare 터널링은 Cloudflare CLI로. uni-auth 등록의 redirect·audience와 프로젝트 설정·런북도 새 주소로 일관되게. CLI 권한과 기존 터널을 확인해 인증된 서비스만 안전하게 공개·검증. 기존 서비스 보호, 운영 서버 재시작은 프로젝트 사용자만. Cloudflare CLI DNS/터널 변경 승인. root·사용자 재시작이 필요하면 정확한 명령과 결과를 보고. 작업·독립 검증·보기 패널 유지."
E-74 완료 조건 6을 대체한다(주소만 바뀜, 나머지 조건 그대로).

- 완료 조건:
  1. 저장소에서 운영 주소 값이 books.betterax.net으로 일관된다: scripts/deploy/library/*·예시 설정·런북·방향 문서(decisions.md E-74, arch-backend.md)의 현재 값. 지난 진행 기록(docs/progress 옛 파일)은 고치지 않고 새 기록이 대체를 밝힌다. `git grep library.betterax`가 지난 기록·상태 기록 밖에서 0건, make lint PASS, 독립 검증 PASS 뒤 병합.
  2. uni-auth 클라이언트 zip-viewer의 redirect_uris·post_logout_redirect_uris·audience가 https://books.betterax.net 기준으로 갱신되고(게이트웨이 관리 API, Hydra 직접 쓰기 금지), GET·Hydra 투영 대조가 일치한다. 다른 클라이언트는 무변경.
  3. books.betterax.net DNS가 기존 터널(gift4me-tunnel)로 연결되고(cloudflared CLI), 기존 레코드·호스트(auth·ohmyassets 등)는 무변경임을 전후 대조로 보인다. ingress 규칙이 생기기 전까지 catch-all 404로 아무것도 노출되지 않는다.
  4. ingress 적용·cloudflared 재시작·:9909 중지·9910 oidc 인스턴스 기동은 사용자 실행: 백업·검증·복구를 포함한 정확한 명령과 기대 출력을 보고한다. 사용자 적용 뒤 공개 검증: 비인증 요청은 모든 데이터 경로에서 401/로그인, 허용 목록 밖 403, 평문 우회 경로 없음, 실제 로그인 1회 성공 — 독립 검증 PASS.
- 허용 범위: zip-viewer 저장소(위 파일), cloudflared CLI의 DNS route(books 1건), uni-auth 게이트웨이 관리 API로 zip-viewer 클라이언트 갱신 1건(운영 가이드 절차로 토큰 획득). 금지: sudo·/etc 쓰기·systemctl·:9909 조작(사용자 몫), 다른 DNS 레코드·다른 클라이언트·uni-auth 설정 변경, 옛 library 레코드 삭제(있으면 보고만), push.
- 탈출 조건: cert.pem이 betterax.net 영역 권한이 없을 때, 관리 API 갱신이 등록 1건 범위를 넘을 때, 기존 서비스에 영향이 생길 때 → [차단].
- 묶음 U-R3: U-R3a 저장소 주소 변경 → U-R3b 운영 준비(CLI 권한·터널 확인, uni-auth 갱신, DNS route, 사용자 명령 준비) → (사용자 적용) → U-R3c 공개 검증. 같은 워커(worker-3)에 순서대로.

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
| U-R3a | U-R3 | 운영 주소 library → books.betterax.net 저장소 반영 | 시작 계약 3 완료 조건 1 | 없음 | scripts/deploy/library/*, docs/decisions.md, docs/arch-backend.md, 런북(새 진행 기록으로 대체) |
| U-R3b | U-R3 | CLI 권한·터널 확인, uni-auth 클라이언트 갱신, DNS route, 사용자 실행 명령 준비 | 시작 계약 3 완료 조건 2·3·4(명령) | U-R3a | docs/progress 새 파일(외부 상태: uni-auth 클라이언트 1건, DNS 1건) |
| U-R3c | U-R3 | 사용자 적용 뒤 공개 검증·실제 로그인 | 시작 계약 3 완료 조건 4(검증) | U-R3b, 사용자 적용 | docs/progress |
| U-R4 | U-R4 | books 운영 실행 스크립트(check·status·start·stop·restart·tunnel·rollback·verify)·자동 테스트·런북 | 시작 계약 4 완료 조건 1~5 | 없음(U-R3a 스크립트 재사용) | scripts/deploy/library/**, docs/progress 새 런북 |
| U-R5 | U-R5 | books 대화형 설치·운영 마법사(books.sh 위)·pty 자동 테스트·런북 | 시작 계약 5 완료 조건 1~6 | U-R4(병합됨) | scripts/deploy/library/**, docs/progress 새 런북 |
