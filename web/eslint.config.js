import pluginVue from 'eslint-plugin-vue'
import { withVueTs, vueTsConfigs } from '@vue/eslint-config-typescript'
import eslintConfigPrettier from 'eslint-config-prettier/flat'

export default withVueTs(
  pluginVue.configs['flat/essential'],
  vueTsConfigs.recommended,
  {
    name: 'app/ignores',
    ignores: ['dist/**', 'coverage/**', 'node_modules/**'],
  },
  eslintConfigPrettier,
)
