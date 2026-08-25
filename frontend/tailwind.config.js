/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        // Panel surface palette. Kept as semantic names so components never
        // hard-code raw hex values.
        surface: {
          DEFAULT: '#ffffff',
          muted: '#f8fafc',
          border: '#e2e8f0',
        },
        brand: {
          50: '#eef4ff',
          100: '#d9e6ff',
          500: '#3b6ef6',
          600: '#2b57d4',
          700: '#2245ab',
        },
      },
    },
  },
  plugins: [],
};
