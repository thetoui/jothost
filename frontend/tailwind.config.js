/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        // Panel surfaces. Semantic names so components never hard-code hex.
        surface: {
          DEFAULT: '#ffffff',
          muted: '#f6f8fb',
          sunken: '#eef2f7',
          border: '#e3e9f0',
          strong: '#cbd5e1',
        },
        // The navigation rail. A dark rail is the strongest visual cue that
        // navigation and content are different things, and it is what makes a
        // control panel read as a control panel rather than a web page.
        rail: {
          DEFAULT: '#16202e',
          hover: '#1f2c3d',
          active: '#26374d',
          border: '#243141',
          text: '#93a4b8',
          bright: '#eaf0f7',
        },
        brand: {
          50: '#eef4ff',
          100: '#d9e6ff',
          200: '#b8d0ff',
          300: '#8db2ff',
          400: '#5c8dfa',
          500: '#3b6ef6',
          600: '#2b57d4',
          700: '#2245ab',
          800: '#1d3a8a',
          900: '#1b3271',
        },
        // Status colours, named by meaning rather than hue, so a change of
        // palette does not require finding every "green" in the codebase.
        ok: {
          50: '#ecfdf5',
          100: '#d1fae5',
          200: '#a7f3d0',
          300: '#6ee7b7',
          500: '#10b981',
          600: '#059669',
          700: '#047857',
          800: '#065f46',
        },
        warn: {
          50: '#fffbeb',
          100: '#fef3c7',
          200: '#fde68a',
          300: '#fcd34d',
          500: '#f59e0b',
          600: '#d97706',
          700: '#b45309',
          800: '#92400e',
        },
        danger: {
          50: '#fef2f2',
          100: '#fee2e2',
          200: '#fecaca',
          300: '#fca5a5',
          500: '#ef4444',
          600: '#dc2626',
          700: '#b91c1c',
          800: '#991b1b',
        },
        // Neutral information: a state that is neither good nor bad, and must
        // not be dressed as either. Used for "queued", "scheduled", "unknown".
        info: {
          50: '#f0f9ff',
          100: '#e0f2fe',
          500: '#0ea5e9',
          600: '#0284c7',
          700: '#0369a1',
          800: '#075985',
        },
      },
      boxShadow: {
        // A small elevation scale. Panels sit at "card"; anything that floats
        // over the page (menu, dialog) sits higher so the stacking reads.
        card: '0 1px 2px 0 rgb(16 32 46 / 0.04), 0 1px 3px 0 rgb(16 32 46 / 0.06)',
        raised: '0 2px 4px -1px rgb(16 32 46 / 0.06), 0 4px 12px -2px rgb(16 32 46 / 0.08)',
        menu: '0 4px 6px -2px rgb(16 32 46 / 0.08), 0 12px 24px -4px rgb(16 32 46 / 0.12)',
        dialog: '0 16px 48px -8px rgb(16 32 46 / 0.24)',
      },
      borderRadius: {
        card: '0.625rem',
      },
      keyframes: {
        // Skeletons sweep rather than pulse: a sweep reads as "content is on
        // its way", where a pulse reads as "something needs attention".
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
        'indeterminate': {
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
