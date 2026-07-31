// PostCSS pipeline for the Vite build (impl-plan §2, WP-05).
//
// Tailwind first (it expands the @tailwind directives in src/styles/base.css),
// then autoprefixer. Nothing else: the token layer is plain custom properties,
// so no nesting or preset-env plugin is required.
export default {
  plugins: {
    tailwindcss: {},
    autoprefixer: {},
  },
}
