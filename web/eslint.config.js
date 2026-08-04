// ESLint 9 flat config. Enforces the binding TypeScript/React conventions of
// impl-plan §5.2 that a compiler cannot: no `any`, no default exports, no
// `rounded-*` utilities, and `fetch` only inside src/api/.
import js from '@eslint/js'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import globals from 'globals'
import tseslint from 'typescript-eslint'

/** Radius utilities that cannot exist: tailwind.config.ts overrides
 *  `borderRadius` to the `--radius-*` token scale {none, DEFAULT, sm, md, lg,
 *  pill, full}, so the xl family and every arbitrary `rounded-[13px]` would
 *  silently no-op (decisions.md D-40 as amended by E-32). The token steps,
 *  `rounded`, `rounded-none` and `rounded-full` are legal. */
const bannedRadius = String.raw`\brounded(-[a-z]+)?-(2xl|3xl|xl|\[)`

const radiusMessage =
  'The radius scale is closed (D-40 as amended by E-32): tailwind.config.ts overrides borderRadius to the --radius-* tokens {none, DEFAULT, sm, md, lg, pill, full}, so rounded-xl|2xl|3xl and arbitrary radii do not exist. Add a token in tokens.css, not a number here.'

const noRadiusUtilities = [
  { selector: `Literal[value=/${bannedRadius}/]`, message: radiusMessage },
  { selector: `TemplateElement[value.raw=/${bannedRadius}/]`, message: radiusMessage },
]

const noDefaultExport = {
  selector: 'ExportDefaultDeclaration',
  message:
    'Named exports only (impl-plan §5.2): one component per file, file name = component name. Default exports break rename refactors and make re-export barrels ambiguous.',
}

export default tseslint.config(
  {
    ignores: [
      'dist/**',
      'node_modules/**',
      'playwright-report/**',
      'test-results/**',
      'blob-report/**',
      'coverage/**',
    ],
  },

  js.configs.recommended,
  ...tseslint.configs.strictTypeChecked,
  ...tseslint.configs.stylisticTypeChecked,

  {
    languageOptions: {
      ecmaVersion: 2022,
      globals: { ...globals.browser, ...globals.es2022 },
      parserOptions: {
        // projectService keeps this working while src/ is still empty and
        // covers files that no tsconfig lists, which a `project` array cannot.
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
    linterOptions: { reportUnusedDisableDirectives: 'error' },
  },

  // ---- application source ------------------------------------------------
  {
    files: ['src/**/*.{ts,tsx}'],
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],

      // `unknown` plus a narrowing guard, never `any` (impl-plan §5.2).
      '@typescript-eslint/no-explicit-any': 'error',
      '@typescript-eslint/consistent-type-imports': [
        'error',
        { prefer: 'type-imports', fixStyle: 'inline-type-imports' },
      ],
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_' },
      ],

      'no-restricted-syntax': ['error', noDefaultExport, ...noRadiusUtilities],

      // Hex colours and radii come from the tokens, never from a literal
      // (ui-spec §1.2/§1.4, D-40/D-41).
      'no-restricted-properties': 'off',
    },
  },

  // ---- the single fetch boundary (impl-plan §5.4, D-44) ------------------
  {
    files: ['src/**/*.{ts,tsx}'],
    ignores: ['src/api/**'],
    rules: {
      'no-restricted-globals': [
        'error',
        {
          name: 'fetch',
          message:
            'src/api/client.ts is the only module that may call fetch (impl-plan §5.4). Use the typed hooks in src/api/queries.ts.',
        },
      ],
    },
  },

  // ---- config files and E2E specs ---------------------------------------
  {
    files: ['*.config.ts', 'e2e/**/*.ts'],
    languageOptions: { globals: { ...globals.node } },
    rules: {
      // Vite, Vitest and Playwright configs are default-export by contract.
      'no-restricted-syntax': ['error', ...noRadiusUtilities],
    },
  },

  // ---- the TypeScript project behind e2e/ -------------------------------
  // playwright.config.ts sets `testDir: './e2e'`, but tsconfig.json is the app
  // project and excludes e2e/, so the project service cannot find a spec and
  // every type-aware rule dies with "was not found by the project service" —
  // one Playwright file was enough to make `pnpm lint` exit 1. Point the parser
  // at tsconfig.e2e.json, which owns exactly these files.
  //
  // `projectService.allowDefaultProject` is not the fix: it hard-errors once
  // more than eight files match, and the §6.3 suite is larger than that.
  {
    files: ['e2e/**/*.ts'],
    languageOptions: {
      parserOptions: {
        projectService: false,
        project: ['./tsconfig.e2e.json'],
        tsconfigRootDir: import.meta.dirname,
      },
    },
  },

  // ---- this file --------------------------------------------------------
  // eslint.config.js is JavaScript and belongs to no tsconfig, so the type-aware
  // rules have nothing to run against. Turn the project service off for it
  // rather than adding a .js file to the app project.
  {
    files: ['**/*.js'],
    languageOptions: {
      globals: { ...globals.node },
      parserOptions: { projectService: false, project: false },
    },
    rules: {
      // Spread, not replace: disableTypeChecked *is* a rules object, and
      // assigning over it would leave every type-aware rule enabled with no
      // type information — which is the error this block exists to prevent.
      ...tseslint.configs.disableTypeChecked.rules,
      'no-restricted-syntax': 'off',
    },
  },
)
