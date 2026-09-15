// Tailwind 4 ships its PostCSS integration as a separate package and includes
// vendor prefixing, so the standalone autoprefixer is no longer listed.
export default {
  plugins: {
    '@tailwindcss/postcss': {},
  },
};
