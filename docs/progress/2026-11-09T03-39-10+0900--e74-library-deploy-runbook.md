timestamp: 2026-11-09T03:39:10+09:00
topic: e74-library-deploy-runbook
type: plan
status: ready-for-user
supersedes:

# library.betterax.net 공개 적용 — 사용자 실행 런북 (U-R2a)

배치 기준(Commander 2026-11-09, E-74 §2.18): **oidc 단일 인스턴스, `127.0.0.1` 바인드**, 인스턴스는 이것 하나. 현재 공개 금지다. 이 문서는 사용자가 순서대로 실행하는 절차이고, `sudo`·서버 재시작·DNS·터널 재시작은 사용자가 직접 한다. 스크립트는 `scripts/deploy/library/`에 있다.

검증 표기: **[실행]** 이 문서를 쓰면서 돌려 봄, **[추론]**, **[미확인]**.

## 실행 전에 알아 둘 것

- **선행 조건: oidc 모드가 들어간 바이너리가 있어야 한다**(E-74 U-B/U-D 병합 뒤 main). 지금 main의 `dist/shelf`는 oidc가 없다. [실행] 이 바이너리를 임시 포트에 무인증으로 띄워 `verify-public.sh`가 FAIL 21건을 내는 것을 확인했다(스크립트가 허술하게 통과시키지 않는다는 근거).
- **지금 `*:9909`에서 shelf가 돌고 있다** [실행: `ss -ltnp`로 확인, U-R1 기록 시점에는 꺼져 있었다]. 설정에 `auth`가 없으면 LAN 누구나 컬렉션을 읽는다. §2.18의 공개 금지 조건에 걸리므로 3단계에서 멈춘다.
- 터널 적용(7단계)은 같은 cloudflared가 서빙하는 **`auth.betterax.net`·`ohmyassets.betterax.net`을 몇 초 끊는다** [추론: cloudflared는 ingress를 재시작으로만 다시 읽는다고 알고 있다, 실제 중단 시간 미확인].
- 데이터 이전은 한 방향이다. 새 바이너리가 처음 `user.db`를 열면 스키마가 3 → 4가 되고, **옛 바이너리는 4를 거부한다**(`ErrSchemaTooNew`). 그래서 3단계에서 백업한다.
- 변수(이후 명령에서 쓴다): 
  ```bash
  REPO=/mnt/data/codejobs/projects/betterax-proto/zip-viewer        # 빌드한 저장소(main)
  CFG=$HOME/shelf.library.yaml                                      # 4단계에서 만드는 설정
  D=$REPO/scripts/deploy/library
  PORT=9910
  ```

## 0. 바이너리 빌드 (권한 불필요)

```bash
cd $REPO && git log --oneline -1          # oidc가 들어간 main인지 확인
make build                                 # dist/shelf (웹 포함)
dist/shelf --version                       # 버전/커밋 확인
```
기대: 에러 없이 `dist/shelf  <bytes>` 출력, `--version`이 방금 커밋을 보임. 실패하면 중단하고 `make build` 로그를 가져온다(서버·터널은 아직 건드리지 않았으니 되돌릴 것 없음).

## 1. 설정 만들기

```bash
cp $D/shelf.library.example.yaml $CFG
$EDITOR $CFG        # roots, data_dir, cache_dir의 <...>를 기존 서버 값으로 채운다
```
등록값(client_id·audience·redirect_url·issuer)은 바꾸지 않는다. `allowed_subs`는 지금 비워 둔다(9단계에서 채운다). 기존 설정(`shelf.yaml`)의 `roots`·`storage`를 그대로 옮기면 인덱스·진행률이 이어진다.

## 2. 점검 (권한 불필요, 읽기 전용)

```bash
$D/preflight.sh --config $CFG --bin $REPO/dist/shelf --port $PORT
```
기대: FAIL 0. `P6`(allowed_subs 비어 있음)은 WARN이 정상이다. `P13`(포트 점유)·`P14`(와일드카드 리스너)가 FAIL이면 3단계를 먼저 한다. 설정 오류는 이 표에서 고친다(FAIL 항목 한 줄에 원인이 있다).

## 3. 기존 서버 중지와 데이터 백업 (사용자 직접)

```bash
ss -ltnp | grep -E ':9909\b'               # 기존 서버의 PID 확인
kill <PID>                                 # 이 shelf를 띄운 방식대로 멈춘다. 확인:
ss -ltnH 'sport = :9909' | wc -l           # 0이어야 한다
DATA=$(python3 -c "import yaml;print(yaml.safe_load(open('$CFG'))['storage']['data_dir'])")
cp -a "$DATA" "$DATA.bak-$(date -u +%Y%m%dT%H%M%SZ)"; ls -d "$DATA".bak-*
```
기대: 9909 리스너 없음, `$DATA.bak-<UTC>`가 생김(`user.db`·`user.db-wal` 포함, 서버가 멈춘 뒤라 일관된 사본). **서버를 멈추기 전에 복사하지 않는다**(WAL 때문에 불일치 사본이 된다). 실패 시: 기존 서버를 원래 방식으로 다시 띄우면 상태는 이전과 같다(아직 아무것도 바꾸지 않았다).

## 4. 새 서버 시작 (사용자 직접, 아직 터널 없음)

```bash
cd $REPO && nohup dist/shelf --config $CFG >> ~/shelf-library.log 2>&1 &
sleep 3; tail -20 ~/shelf-library.log
ss -ltnp | grep -E ":$PORT\b"
```
기대: `127.0.0.1:$PORT`만 listen, 로그에 오류 없음, `user.db` 스키마 4로 이전됨:
```bash
python3 -c "import sqlite3,sys;print(sqlite3.connect('file:$DATA/user.db?mode=ro',uri=True).execute(\"select value from meta where key='schema_version'\").fetchone())"   # ('4',)
```
기동 실패(exit 2)면 로그의 한 줄이 이유다(설정 검사, §2.19) — 고치고 다시 시작. 데이터가 의심되면 서버를 멈추고 `rm -r $DATA && mv $DATA.bak-<UTC> $DATA`로 되돌린 뒤 이전 바이너리로 시작한다.

## 5. 로컬 우회 경로 검증 (권한 불필요)

```bash
$D/verify-public.sh --local --port $PORT
```
기대: **FAIL 0**. 세션 없이 `/`·`/api/*`·쪽·썸네일·표지·SPA 자산이 401/로그인 화면, `/api/health?verbose=1`에 verbose 블록 없음, 교차 출처 POST가 403 `cross_origin`, LAN 주소로 접속 거부, 와일드카드 리스너 없음. FAIL 하나라도 있으면 **여기서 멈추고 보고한다. 6단계로 가지 않는다.** 서버를 멈추면(`kill`) 외부에 보이는 것은 없다.

## 6. DNS (사용자 직접, 되돌리기 쉬움)

```bash
$D/dns-route.sh
```
호스트명을 입력해 확인한다. 기대: `cloudflared tunnel route dns`가 CNAME 추가를 보고. 권한 오류면 대시보드 DNS에서 `library` CNAME → `178647b8-b76b-49a3-8b8f-3ca06f191226.cfargotunnel.com`(프록시 켬) [미확인: cert.pem이 `betterax.net`에 권한이 있는지는 실행해 봐야 안다]. 터널 규칙이 없는 동안 이 호스트는 catch-all 404를 준다. 되돌리기: 대시보드에서 `library` CNAME 삭제.

## 7. 터널 적용 — 중단 시점 (사용자 직접, sudo)

```bash
sudo $D/tunnel-apply.sh --service http://127.0.0.1:$PORT --restart
```
스크립트가 하는 일: `/etc/cloudflared/config.yml`을 `.bak-<UTC>`로 백업 → ohmyassets 규칙 뒤·catch-all 앞에 규칙 삽입 → `ingress validate`와 규칙 확인 네 가지(library → `:$PORT`, auth → 8443, ohmyassets → 18100, 그 밖 → 404) → 하나라도 틀리면 **백업을 되돌리고 재시작하지 않고** 종료 → 모두 통과하고 `--restart`가 있을 때만 `systemctl restart cloudflared`. 재시작하는 순간 **auth·ohmyassets가 몇 초 끊긴다.** `--restart` 없이 먼저 돌려 표만 확인하고(파일은 바뀐다), 시간을 골라 `sudo systemctl restart cloudflared`를 따로 해도 된다.

기대: 표 전부 PASS, `cloudflared is active`. 확인:
```bash
journalctl -u cloudflared -n 30 --no-pager | grep -i 'registered tunnel connection'   # 연결 재등록
curl -sI https://auth.betterax.net/.well-known/openid-configuration | head -1          # 이전과 같은 응답
curl -sI https://ohmyassets.betterax.net/ | head -1
```
실패·이상 시 즉시: `sudo $D/tunnel-rollback.sh --backup /etc/cloudflared/config.yml.bak-<UTC> --restart` (apply가 마지막 줄에 정확한 명령을 출력한다). 롤백은 validate를 통과해야 재시작한다.

## 8. 공개 우회 경로 검증 (권한 불필요)

```bash
$D/verify-public.sh --public
```
기대: FAIL 0. 5단계의 점검을 `https://library.betterax.net`로 반복하고, 더해서 `oidc/start`의 Set-Cookie가 Secure·HttpOnly이고 `cf-cache-status`가 `HIT`가 아님을 본다. 하나라도 FAIL이면 **즉시 터널을 되돌린다**(7단계의 롤백 명령) 그리고 보고한다. 로컬 FAIL 0·공개 FAIL은 Cloudflare 쪽(캐시 규칙, `Cache-Control`)이 원인이다.

## 9. 실제 로그인과 허용 목록 (사용자 직접)

1. 브라우저로 `https://library.betterax.net/`를 연다 → 서버가 그린 로그인 화면 → `uni-auth로 로그인` → uni-auth에서 로그인.
2. 첫 로그인은 **403 화면**이 정상이다(허용 목록이 비어 있다). 화면의 `sub`를 복사한다.
3. 서버를 멈추고 설정에 추가한 뒤 다시 시작:
   ```bash
   $EDITOR $CFG      # allowed_subs: ["<sub>"], admin_subs: ["<sub>"], legacy_owner_sub: "<sub>"
   kill $(pgrep -x shelf); cd $REPO && nohup dist/shelf --config $CFG >> ~/shelf-library.log 2>&1 &
   ```
4. 다시 로그인 → 서재가 보이고, 이전 읽기 진행률이 보인다(`legacy_owner_sub`가 맞으면 한 번 이관된다, §2.14).

기대: 서재 표시, 관리 기능은 재인증(15분) 뒤. 이상하면 허용 목록 변경만 되돌리고 재시작하면 된다. **다른 가족은 uni-auth에 각자 가입(이메일 검증까지)한 뒤 같은 방식으로 `sub`를 받아 추가한다.**

## 되돌리기

| 상황 | 절차 |
| --- | --- |
| 터널만 되돌림 (공개 중단) | `sudo $D/tunnel-rollback.sh --backup /etc/cloudflared/config.yml.bak-<UTC> --restart` (auth·ohmyassets가 몇 초 끊김). 되돌린 뒤 `library`는 catch-all 404. |
| 공개를 멈추고 서버도 내림 | 위 롤백 → `kill $(pgrep -x shelf)`. DNS는 남아도 404라 무해, 정리하려면 대시보드에서 CNAME 삭제. |
| password 모드/인증 없음으로 돌아감 | **먼저 터널을 되돌린다**(§2.18: 이 모드는 공개 금지). 서버를 멈추고 `shelf auth restore-owner --sub <sub> --config $CFG`(서버 정지 상태에서만)로 사용자 데이터를 옛 테이블에 복사한 뒤, 설정을 `mode: password`로 바꿔 LAN에서만 시작. |
| 데이터를 이전 상태로 | 서버 정지 → `rm -r $DATA && mv $DATA.bak-<UTC> $DATA` → **옛 바이너리**로 시작(v4 파일은 옛 바이너리가 거부하므로 백업의 v3 `user.db`와 짝을 맞춘다). 백업 이후의 진행률은 사라진다. |

## 열린 문제

- 로그아웃 뒤 복귀: uni-auth 투영기가 `post_logout_redirect_uris`를 Hydra에 넣지 않는 것으로 보인다(U-R1 기록). 로그아웃 시 인증 서버 화면에 머물 수 있다 [미확인].
- 공개 터널 경유 응답 시간(NFR-PRF-001은 로컬 기준)은 U-E에서 잰다.
- cloudflared 2026.10.7 권고 경고가 있다(이 작업 범위 밖).
