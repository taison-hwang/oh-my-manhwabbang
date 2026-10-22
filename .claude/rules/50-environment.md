# 50 — 환경 제약과 안전 수칙

## 데이터와 사용자 서버

- 미디어 볼륨 `/mnt/big-data/pds/taison-data/02. books/01. mangga`는 **읽기 전용**이다. 어떤 경우에도 쓰지 않는다.
- 사용자가 띄운 서버(`:9909`)는 사용자 것이다. `GET` 조회는 자유롭게 하되, 쓰기·전량·장시간 작업
  (예: `POST /api/scan {"full":true}`)은 먼저 묻는다. **재시작은 사용자만 한다.**
- 진단은 가장 좁은 도구부터: 임시 Go 프로브 테스트(끝나면 삭제), `index.db`를 `file:...?mode=ro`로 조회(sqlite3 CLI가 없으므로 python3).
- 확인용 서버는 9909가 아닌 포트에 띄운다. 내가 띄운 프로세스는 **받은 pid로만** 종료한다.
  패턴으로 `pkill`/`kill` 하지 않는다 — 사용자의 재색인을 끊은 적이 있다.

## 빌드 환경

- `make`를 쓴다. `go`를 직접 쓸 때는 Makefile의 `GOENV` 줄(`GOPROXY`·`GOSUMDB`·`GOTOOLCHAIN=auto`)을 앞에 붙인다.
  이 머신의 `GOPROXY`는 비어 있다. `GOTOOLCHAIN=local`은 다른 방식으로 실패한다.
- `~/.config/go/env`에는 자격 증명이 들어 있다. 내용을 출력하지 않는다.
- 제품 빌드는 `CGO_ENABLED=0`, 레이스 테스트만 `CGO_ENABLED=1`.
- 릴리스 크기 예산은 `internal/buildinfo/release_budget_test.go`가 지킨다. 의존성·에셋을 넣기 전에 여유를 확인하고,
  크기를 비교하기 전에 `go version dist/shelf`로 툴체인이 같은지부터 본다.

## 공유 작업 트리

- 메인 트리는 다른 세션이 함께 쓴다. 남의 미커밋 변경을 날리는 명령(`git checkout -- <path>`, `git restore`, `git stash`,
  `git reset --hard`)을 쓰지 않는다. 뮤테이션과 원복은 스크래치 사본에서 한다.
- git worktree는 **`<repo>/.worktrees/<이름>` 아래에만** 만든다(사용자 지시 2026-10-18). cwd를 옮기지 말고 절대 경로로 다룬다.
- 저장소 안에 부산물을 남기지 않는다(스크래치 디렉터리 사용, `PYTHONDONTWRITEBYTECODE=1`).
  e2e가 도는 동안 `data_dir`/`cache_dir` 밖에서 파일이 생기거나 지워지면 `scripts/e2e.sh` 9단계가 실패한다.
  저장소에 새 파일을 만들거나 지울 때는 같은 트리를 쓰는 세션에 먼저 알린다.
