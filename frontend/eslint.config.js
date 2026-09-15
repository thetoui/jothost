import js from '@eslint/js';
import globals from 'globals';
import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';
import tseslint from 'typescript-eslint';

export default tseslint.config(
  { ignores: ['dist', 'coverage', 'node_modules'] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2022,
      globals: { ...globals.browser, ...globals.node },
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      // eslint-plugin-react-hooks 7's recommended set folds in the React
      // Compiler's rules, and ESLint 10's recommended adds no-useless-assignment.
      // Those are new opinions, not the policy this project adopted — the config
      // has always been rules-of-hooks and exhaustive-deps — so the tooling is
      // upgraded here without quietly adopting a stricter ruleset with it.
      // Turned off deliberately, each a separate decision to make later:
      //   - set-state-in-effect / refs: the Compiler rules, which fire on
      //     legitimate form-sync effects and ref reads throughout the panel;
      //   - no-useless-assignment: false-positive on the defensive
      //     `let x = null; try { x = … } catch {}` pattern, where the null is
      //     the value on the throw path and is read afterwards.
      'react-hooks/set-state-in-effect': 'off',
      'react-hooks/refs': 'off',
      'no-useless-assignment': 'off',
      'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
      '@typescript-eslint/no-explicit-any': 'error',
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_' }],
      // Business logic belongs in hooks and services, not components
      // (CLAUDE.md section 10); direct fetch calls in components are a
      // common way that rule gets broken.
      'no-restricted-globals': ['error', { name: 'fetch', message: 'Use the API service layer in src/services instead.' }],
    },
  },
);
