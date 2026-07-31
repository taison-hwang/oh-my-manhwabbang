/**
 * A minimal CSS block scanner, used by the token tests.
 *
 * The token layer is the one place in the product where being *approximately*
 * right is not good enough: `--color-bg` resolving to the wrong value in a
 * nested scope is what makes the viewer light inside a light app theme, and
 * jsdom does not implement custom-property resolution well enough to catch it
 * with `getComputedStyle`. So the tests read the stylesheet and assert on its
 * structure directly — which also catches the failure mode `getComputedStyle`
 * could never see: a selector too narrow to match a nested element.
 *
 * Not a general CSS parser. It handles exactly what the token and base sheets
 * contain: rules, at-rule blocks (`@media`, `@layer`) nested one or two deep,
 * and declarations.
 */

export interface CssRule {
  /** The selector text, or the at-rule prelude. */
  selector: string
  /** Everything between the braces. */
  body: string
  /** Offset of the rule's opening brace in the (comment-stripped) source. */
  start: number
  /** Enclosing at-rule preludes, outermost first. */
  context: string[]
}

/** Replaces comments with spaces so offsets stay meaningful. */
export function stripComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, (m) => ' '.repeat(m.length))
}

function scan(source: string, offset: number, context: string[]): CssRule[] {
  const rules: CssRule[] = []
  let depth = 0
  let preludeStart = 0
  let bodyStart = 0

  for (let i = 0; i < source.length; i++) {
    const ch = source[i]
    if (ch === '{') {
      if (depth === 0) bodyStart = i
      depth += 1
    } else if (ch === '}') {
      depth -= 1
      if (depth === 0) {
        rules.push({
          selector: source.slice(preludeStart, bodyStart).trim(),
          body: source.slice(bodyStart + 1, i),
          start: offset + bodyStart,
          context,
        })
        preludeStart = i + 1
      }
    }
  }
  return rules
}

/** One level of rules. At-rule bodies are returned unexpanded. */
export function topLevelRules(css: string): CssRule[] {
  return scan(stripComments(css), 0, [])
}

/**
 * Every rule in the sheet, at-rules expanded, each carrying the chain of
 * enclosing preludes in `context`.
 */
export function allRules(css: string): CssRule[] {
  const out: CssRule[] = []
  const walk = (rules: CssRule[]): void => {
    for (const rule of rules) {
      out.push(rule)
      if (rule.selector.startsWith('@') && rule.body.includes('{')) {
        walk(scan(rule.body, rule.start + 1, [...rule.context, rule.selector]))
      }
    }
  }
  walk(topLevelRules(css))
  return out
}

/**
 * Custom-property declarations inside a rule body, including any nested one
 * level down — which is what makes `@media { :root { --x: … } }` readable as a
 * single "this block sets --x" fact.
 */
export function customProperties(body: string): Map<string, string> {
  const out = new Map<string, string>()
  const re = /(--[a-z0-9-]+)\s*:\s*([^;]+);/gi
  let m: RegExpExecArray | null = re.exec(body)
  while (m !== null) {
    const name = m[1]
    const value = m[2]
    if (name !== undefined && value !== undefined) out.set(name, value.trim().replace(/\s+/g, ' '))
    m = re.exec(body)
  }
  return out
}

/** Finds the first rule whose selector list contains `needle`. */
export function findRule(rules: CssRule[], needle: string): CssRule | undefined {
  return rules.find((r) => r.selector.includes(needle))
}
