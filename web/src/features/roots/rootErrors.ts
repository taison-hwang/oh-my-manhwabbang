import { isApiError } from '../../api/errors'

/**
 * A failed `POST /api/roots` / `DELETE /api/roots/{name}`, as a sentence the
 * user can act on (amendment **A-11**, ruling **E-26**).
 *
 * **Why this file exists at all.** E-26 decision 4 makes every rejection "a
 * `400` naming the rule it broke", and arch §7.4 tabulates nine of them. That
 * table is the entire justification for validating on the server: the client
 * cannot check whether a path exists on the server's host, is a directory, is
 * readable, or overlaps a root it may not even be able to see. Collapsing the
 * answer back into one 실패했습니다 throws away the only thing the round trip
 * bought — the user would learn that something is wrong and not which of nine
 * things. So each `detail.reason` gets its own sentence, and the two that carry
 * `detail.conflicts_with` name the root they collided with, because §7.4
 * deliberately does **not** echo the rejected path back and that name is the
 * only concrete thing such a message can hold.
 *
 * The `message` on the envelope is not used. §7.2 types it "English,
 * human-readable, safe to display", and this is a Korean interface; `code` and
 * `detail.reason` are the machine-readable halves and are what a client is
 * meant to branch on.
 */

/** Which verb failed. The two share most of their statuses and none of their remedies. */
export type RootOperation = 'add' | 'remove'

/** `detail.reason` on a `400` from `POST /api/roots` (arch §7.4's table). */
const ADD_REASONS: Readonly<Record<string, string>> = {
  missing: '루트 경로를 입력하세요.',
  not_absolute: '절대 경로를 입력하세요. 서버에서 / 로 시작하는 전체 경로여야 합니다.',
  does_not_exist: '서버에 그 경로가 없습니다. 경로를 다시 확인하세요.',
  not_a_directory: '그 경로는 폴더가 아닙니다. 폴더를 지정하세요.',
  not_readable: '서버가 그 폴더를 읽을 수 없습니다. 폴더 권한을 확인하세요.',
  too_long: '이름표가 너무 깁니다. 128바이트 이하로 줄이세요.',
  control_characters: '이름표에 제어 문자를 쓸 수 없습니다.',
  contains_storage:
    '이 폴더 안에 이 앱의 데이터·캐시 폴더가 있습니다. 다른 폴더를 지정하세요.',
}

/**
 * `detail.reason` on a `403`. Three conditions are folded into one capability
 * boolean for the UI (arch §7.8), but the *remedy* differs, so the refusal
 * itself is unfolded again here — that is what §7.4 says the reason is for.
 */
const FORBIDDEN_REASONS: Readonly<Record<string, string>> = {
  disabled:
    '이 서버는 루트 편집이 꺼져 있습니다. 설정 파일에서 server.allow_root_editing을 true로 바꾸고 다시 시작하세요.',
  no_config_file: '이 서버는 설정 파일 없이 실행 중이라 루트 목록을 편집할 수 없습니다.',
  config_inside_root:
    '설정 파일이 루트 폴더 안에 있어 편집할 수 없습니다. 설정 파일을 루트 밖으로 옮기고 다시 시작하세요.',
  // A-12 / ruling E-40. Both come from `GET /api/browse` and neither is a
  // failure of the add — the path can still be typed, which is what the picker
  // falls back to.
  no_browse_bases:
    '폴더 찾아보기를 쓰려면 설정 파일의 server.browse_bases에 탐색할 폴더를 지정해야 합니다. 경로를 직접 입력할 수는 있습니다.',
  outside_browse_bases:
    '그 폴더는 이 서버가 탐색하도록 지정된 범위 밖입니다. server.browse_bases를 확인하세요.',
}

/**
 * `detail.reason` on a `409` from either verb (arch §7.4).
 *
 * §7.4's status tables describe the *conditions* in prose — "it is the last root
 * … or the file on disk can no longer be parsed" — and the server discriminates
 * them with a reason, the same way it does for `400` and `403`. Every `409` the
 * two endpoints raise carries one, so the combined sentence this table replaced
 * is now only the answer to a `409` that arrives without one.
 *
 * `not_a_block_sequence` is the row that forced the table. A `roots: [{...}]`
 * written in YAML flow style is *valid YAML*, and `GET /api/roots` and
 * `GET /api/settings` are reading that very file successfully on this screen —
 * so telling the user it cannot be read, while they are looking at its contents,
 * is both wrong and unactionable. What the writer cannot do is splice a new
 * entry into a one-line sequence, and the remedy is to rewrite the list in block
 * style, which is the only thing this message has to say.
 *
 * `duplicate` appears here **and** in the `400` table, and means different
 * things: at `400` the path is already a root, at `409` the *name* was taken
 * between this request reading the file and writing it. Different remedies, so
 * different sentences, and they never collide because the status separates them.
 */
const CONFLICT_REASONS: Readonly<Record<string, string>> = {
  not_a_block_sequence:
    '설정 파일의 roots: 목록이 대괄호 한 줄 형식([...])이라 항목을 넣고 뺄 수 없습니다. roots: 아래에 “- path: …” 형태의 여러 줄 목록으로 바꾼 뒤 다시 시도하세요.',
  unparseable:
    '설정 파일의 YAML 문법이 깨져 있어 편집하지 않았습니다. 파일을 고친 뒤 다시 시도하세요.',
  file_missing:
    '설정 파일이 서버가 읽어들인 위치에 더 이상 없습니다. 파일을 되돌리거나, 서버를 다시 시작해 현재 파일을 읽게 하세요.',
  last_root:
    '마지막 루트는 제거할 수 없습니다. 서버가 시작하려면 루트가 하나 이상 필요하니, 새 루트를 먼저 추가하세요.',
  duplicate:
    '같은 이름의 루트가 그 사이에 추가되었습니다. 목록을 다시 읽은 뒤 시도하세요.',
}

/**
 * Why the picker cannot offer a directory — `BrowseEntry.reason` (amendment
 * **A-12**, ruling **E-40**).
 *
 * The server computes `selectable` from §7.4's own rules, so these are the same
 * refusals `POST /api/roots` would have given, arriving *before* the click
 * instead of after it. They are shorter than the `400` sentences above on
 * purpose: this text sits inline on a row the user has not chosen yet, where the
 * remedy is "pick a different folder" and is already obvious from the context.
 * The long form still belongs to the alert that follows a real failure.
 *
 * `conflicts_with` is not available here — a listing would have to carry a root
 * name per row — so `duplicate` and `overlaps` name no root, which is why they
 * do not go through `conflictMessage`.
 */
const BROWSE_REASONS: Readonly<Record<string, string>> = {
  duplicate: '이미 등록된 루트',
  overlaps: '기존 루트의 상위·하위 폴더',
  contains_storage: '앱 데이터·캐시 폴더가 안에 있음',
  does_not_exist: '지금 접근할 수 없음',
  not_readable: '읽을 수 없음',
}

/** `null` for a selectable entry, else the short inline reason. */
export function browseReasonLabel(reason: string | null): string | null {
  if (reason === null) return null
  return BROWSE_REASONS[reason] ?? '선택할 수 없음'
}

function conflictMessage(reason: string, conflictsWith: string | null): string | null {
  const named = conflictsWith ?? ''
  if (reason === 'duplicate') {
    return named === ''
      ? '이미 등록된 폴더입니다.'
      : `이미 ‘${named}’ 루트로 등록된 폴더입니다.`
  }
  if (reason === 'overlaps') {
    return named === ''
      ? '기존 루트의 상위 또는 하위 폴더입니다. 같은 파일이 두 루트에 속할 수 없습니다.'
      : `기존 루트 ‘${named}’의 상위 또는 하위 폴더입니다. 같은 파일이 두 루트에 속할 수 없습니다.`
  }
  return null
}

/**
 * The message to show beside the control that failed.
 *
 * Never returns an empty string: an alert with nothing in it is worse than no
 * alert, because the control looks like it did nothing at all.
 */
export function rootErrorMessage(error: unknown, operation: RootOperation): string {
  if (!isApiError(error)) {
    // A transport failure, an abort, or anything else that never reached the
    // server. There is no `detail` to read and no rule that was broken.
    return '서버에 연결하지 못했습니다. 잠시 후 다시 시도하세요.'
  }

  const reason = error.reason ?? ''

  switch (error.status) {
    case 400: {
      if (operation === 'remove') {
        // §7.4: `{name}` failed `[a-zA-Z0-9._-]{1,64}`. Not reachable from a
        // name this screen received from `GET /api/roots`, so it means the two
        // disagree about what a root name is.
        return '루트 이름이 올바르지 않아 제거하지 못했습니다.'
      }
      return (
        conflictMessage(reason, error.conflictsWith) ??
        ADD_REASONS[reason] ??
        '경로를 확인하세요. 서버가 이 경로를 루트로 받아들이지 않았습니다.'
      )
    }
    case 403:
      return FORBIDDEN_REASONS[reason] ?? '이 서버는 루트 목록 편집을 허용하지 않습니다.'
    case 404:
      // Only `DELETE` produces this: the root is not in the file on disk, so a
      // hand-edit or an earlier removal got there first (§7.4).
      return '설정 파일에 없는 루트입니다. 이미 제거되었을 수 있어 목록을 다시 읽었습니다.'
    case 409:
      // §7.4 discriminates every one of these with `detail.reason`, so the
      // combined sentence below is no longer the answer — it is only the
      // fallback for a `409` that arrives without one. It stays because
      // guessing between the causes from the row count would be worse:
      // `GET /api/roots` may list roots the file does not contain. It says
      // 편집할 수 없는 상태 rather than 읽을 수 없다, because the commonest way to
      // reach it is a file the server reads perfectly and cannot splice.
      return (
        CONFLICT_REASONS[reason] ??
        (operation === 'remove'
          ? '제거할 수 없습니다. 마지막 루트이거나, 설정 파일을 편집할 수 없는 상태입니다.'
          : '설정 파일을 편집할 수 없어 아무것도 쓰지 않았습니다. 파일을 확인한 뒤 다시 시도하세요.')
      )
    case 500:
      // §8.4 keeps the path out of the message, and the client already holds it
      // as `Settings.server.config_path` — which the panel prints beneath.
      return '설정 파일을 쓰지 못했습니다. 파일 권한과 디스크 여유 공간을 확인하세요.'
    default:
      return '요청을 처리하지 못했습니다. 잠시 후 다시 시도하세요.'
  }
}
