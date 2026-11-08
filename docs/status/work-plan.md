# 작업 계획

갱신: 2026-11-08T23:36:31+09:00

## 시작 계약

요청(2026-11-08 사용자, Commander t1h 경유): "남은 PHASE의 개발을 모두 완료, 자율주행, 워커는 보기 패널로".

- 완료 조건:
  1. R-D 묶음이 main에 병합되고, 병합한 main에서 herdr-check가 PASS다.
  2. (사용자 결정 2026-11-08 22:5x로 변경) 전량 test-int-full은 하지 않는다. 최종 main과 b3468ab 사이에 제품 코드(internal·web·cmd) 차이가 없음을 `git diff --stat b3468ab main -- internal web cmd`로 확인하고, E-69의 표본·합성·실 e2e 근거(gates 5/5)를 기능 확인 근거로 쓴다.
  3. prd §9 3차 로드맵 중 아직 하지 않은 여덟 기능에 대해 사용자 결정을 받았다. 착수하기로 한 기능은 판정(E-n) 초안을 사용자가 확정한 뒤 새 계약으로 진행한다. 확정 전에는 구현하지 않는다(intent.md 사용자 승인, E-67 §2.2).
- 허용 범위: `docs/decisions.md`(E-69 §4·§5), `docs/HANDOFF.md`, `AGENTS.md` 프로젝트 규칙 절(harness 표시 밖), `docs/progress/` 새 파일, test-int-full로 드러난 결함의 수정 코드·테스트. 실 컬렉션은 읽기만 하고, 쓰기는 TMPDIR 스크래치에만 한다.
  하면 안 되는 것: 원격 push, :9909 서버 조작·재시작, 옛 worktree 5개·`.backup-harness/`·브랜치 삭제, prd·intent·arch 계약 변경, 3차 기능 구현, 크기 예산 상향, 새 런타임 의존성.
- 탈출 조건: test-int-full 실패가 재배정 2번과 재계획 뒤에도 남을 때, 수정이 계약·요구사항 변경을 요구할 때, 메모리 가드(MemAvailable < 6 GB)로 전량 실행을 못 할 때 근거와 함께 Commander에 보고한다.

## 의존 묶음

- R-F(검사 신뢰성): R-F1 플레이크 테스트를 시계 주입으로 결정적으로. 코드 변경이라 독립 검증 뒤 병합.

- R-D(문서): R-D1 → R-D2 → R-D3. 같은 워커가 순서대로 한다(첫 묶음).
- R-V(검증): R-V1 취소(사용자 결정) → R-V3 과거 문제 파일 대상 격리 읽기 검증(사용자 지시, worker-3 이어서). 실패하면 R-V2(수정)를 같은 워커에 잇는다.
- R-R(3차 로드맵 결정): R-R2(판정 초안, worker-2, R-D와 파일 겹침 없음) → R-R1(사용자 결정). 확정 전 구현 없음.

## 작업

| 작업 ID | 묶음 | 내용 | 수용 기준 | 의존 | 바꿀 파일·모듈 |
| --- | --- | --- | --- | --- | --- |
| R-V1 | R-V | (취소) 실행 중인 test-int-full 안전 중단·정리 | 남은 go test 프로세스 0, heavy.lock 해제, .worktrees/r-v1 제거 | 없음 | 없음 |
| R-F1 | R-F | TestWriter_batching_commitsOnceTheBatchAges 시계 주입 | -count=50 -race 통과, GOMAXPROCS=1 통과, 뮤테이션 2개 RED, herdr-check PASS, 독립 검증 PASS | 없음 | internal/index/write.go, writer_age_test.go, export_test.go |
| R-V3 | R-V | 과거 문제 파일을 유형별로 묶어 유형당 대표 1건(최대 2건)만 격리 읽기 검증 | docs/progress/*--targeted-read-verification.md에 선정 기준·유형 표·대표별 현존·명령·결과·미확인 유형과 로그 경로. :9909 쓰기·재시작 0 | R-V1 중단 | docs/progress/ 새 파일 |
| R-R3 | R-R | E-74를 사용자 결정(계정 = 이 기계의 uni-auth)에 맞춰 구체 계약 초안으로 | docs/progress/*--e74-uni-auth-contract-draft.md에 uni-auth 연동 사실(파일:행·GET 결과)·계약 초안(흐름·신원·인가·사용자별 분리·스키마 v4 가안·이관·A-20 가안·장애 동작)·충돌 문서·결정 질문, 독립 리뷰 기록. uni-auth·:9909 쓰기 0 | R-R2 | docs/progress/ 새 파일 |
| R-R1 | R-R | 3차 로드맵 여덟 기능 착수 여부 | 사용자 결정이 decisions.tsv에 기록됨 | R-R2, 사용자 | 없음 |
