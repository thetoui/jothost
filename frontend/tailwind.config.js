/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      fontFamily: {
        // The redesign's technical, operations-desk voice. Fira Sans carries
        // body and UI text; Fira Code carries anything numeric, code, or a
        // heading that wants to read like a terminal. Loaded in styles/index.css.
        sans: ['"Fira Sans"', 'system-ui', '-apple-system', 'Segoe UI', 'Roboto', 'sans-serif'],
        mono: ['"Fira Code"', 'ui-monospace', 'SFMono-Regular', 'Menlo', 'monospace'],
      },
      colors: {
        // ── Dark redesign ────────────────────────────────────────────────
        // The whole panel is dark now. Token NAMES are unchanged so every
        // existing usage keeps working; only the values moved. Page sits at
        // surface.muted, cards lift to surface.DEFAULT above it, wells recess
        // to surface.sunken below it.
        surface: {
          DEFAULT: '#141f38', // cards / panels
          muted: '#0e1730', // the page itself
          sunken: '#0a1322', // wells, nested panels, inputs
          border: '#26344c',
          strong: '#3a4d70',
        },
        // The navigation rail: darker than the page so it reads as chrome.
        rail: {
          DEFAULT: '#0a1018',
          hover: '#141f34',
          active: '#182a44',
          border: '#1b2740',
          text: '#93a4be',
          bright: '#eef3fb',
        },
        // Text. New semantic scale the 600+ former `text-slate-*` usages were
        // migrated onto: strong=headings, DEFAULT=body, muted=labels,
        // dim=secondary, faint=disabled.
        ink: {
          DEFAULT: '#d5deee',
          strong: '#f3f7fd',
          muted: '#95a6c0',
          dim: '#6d7f9b',
          faint: '#4c5d78',
        },
        // Borders and dividers as their own token, off the surface scale.
        line: {
          DEFAULT: '#26344c',
          strong: '#3a4d70',
        },
        // Terminal / code / log surfaces stay the darkest thing on screen.
        console: {
          DEFAULT: '#080d18',
          fg: '#d3ddec',
        },
        // ── Accent + status ──────────────────────────────────────────────
        // Ramps are INVERTED for a dark UI: low shades are dark background
        // tints, mid (400/500) is the vivid hue for dots/borders/rings, high
        // shades (600–800) are light and used for text. Button *fills* use the
        // vivid 500 explicitly (see controlStyles).
        brand: {
          50: '#0c2416',
          100: '#123a24',
          200: '#1c4d31',
          300: '#7ee6a6',
          400: '#34d17f',
          500: '#22c55e',
          600: '#86efac',
          700: '#bbf7d0',
          800: '#dcfce7',
          900: '#f0fdf4',
        },
        ok: {
          50: '#0a2417',
          100: '#0f3a24',
          200: '#155e38',
          300: '#6ee7b7',
          400: '#34d399',
          500: '#10b981',
          600: '#6ee7b7',
          700: '#a7f3d0',
          800: '#d1fae5',
          900: '#ecfdf5',
        },
        warn: {
          50: '#2a1c0a',
          100: '#3a2610',
          200: '#5b3d14',
          300: '#fcd34d',
          400: '#fbbf24',
          500: '#f59e0b',
          600: '#fcd34d',
          700: '#fde68a',
          800: '#fef3c7',
          900: '#fffbeb',
        },
        danger: {
          50: '#2a1113',
          100: '#3a1618',
          200: '#5b1d1d',
          300: '#fca5a5',
          400: '#f87171',
          500: '#ef4444',
          600: '#fca5a5',
          700: '#fecaca',
          800: '#fee2e2',
          900: '#fef2f2',
        },
        info: {
          50: '#08202f',
          100: '#0c2c40',
          200: '#0f3b56',
          300: '#7dd3fc',
          400: '#38bdf8',
          500: '#0ea5e9',
          600: '#7dd3fc',
          700: '#bae6fd',
          800: '#e0f2fe',
          900: '#f0f9ff',
        },
      },
      boxShadow: {
        // Deeper than the old light-theme scale: on a dark ground separation
        // comes from shadow and border together, not a faint grey edge.
        card: '0 1px 2px 0 rgb(0 0 0 / 0.30), 0 1px 3px 0 rgb(0 0 0 / 0.35)',
        raised: '0 2px 6px -1px rgb(0 0 0 / 0.40), 0 8px 20px -4px rgb(0 0 0 / 0.45)',
        menu: '0 6px 12px -2px rgb(0 0 0 / 0.45), 0 16px 32px -6px rgb(0 0 0 / 0.55)',
        dialog: '0 24px 60px -12px rgb(0 0 0 / 0.65)',
      },
      borderRadius: {
        card: '0.75rem',
      },
      keyframes: {
        shimmer: {
          '100%': { transform: 'translateX(100%)' },
        },
        'fade-in': {
          from: { opacity: '0' },
          to: { opacity: '1' },
        },
        'dialog-in': {
          from: { opacity: '0', transform: 'translateY(8px) scale(0.98)' },
          to: { opacity: '1', transform: 'translateY(0) scale(1)' },
        },
        indeterminate: {
          '0%': { transform: 'translateX(-100%) scaleX(0.35)' },
          '50%': { transform: 'translateX(20%) scaleX(0.6)' },
          '100%': { transform: 'translateX(100%) scaleX(0.35)' },
        },
      },
      animation: {
        shimmer: 'shimmer 1.6s infinite',
        'fade-in': 'fade-in 120ms ease-out',
        'dialog-in': 'dialog-in 160ms cubic-bezier(0.16, 1, 0.3, 1)',
        indeterminate: 'indeterminate 1.4s ease-in-out infinite',
      },
    },
  },
  plugins: [],
};
