// Run from the repository root: node docs/previews/recording-setup/build.mjs
import { build } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import { writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
const entry = fileURLToPath(new URL('./main.ts', import.meta.url));
const result = await build({
  configFile:false,
  plugins:[tailwindcss(),svelte()],
  define:{__CASSINI_OPERATOR_BASE_PATH__:JSON.stringify('/operator')},
  build:{write:false, minify:true, cssCodeSplit:false, lib:{entry,name:'CassiniSetupPreview',formats:['iife']},rollupOptions:{output:{inlineDynamicImports:true}}},
});
const output = (Array.isArray(result)?result[0]:result).output;
const js=output.filter(x=>x.type==='chunk').map(x=>x.code).join('\n');
const css=output.filter(x=>x.type==='asset'&&x.fileName.endsWith('.css')).map(x=>x.source).join('\n');
const html=`<!doctype html><html lang="en" data-theme="light"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Cassini · Recording setup walkthrough</title><style>${css.replaceAll('</style','<\\/style')}</style></head><body><div id="app"></div><script>${js.replaceAll('</script','<\\/script')}</script></body></html>`;
const path=new URL('../../recording-setup-preview.html',import.meta.url);
await writeFile(path,html);
console.log(`Standalone HTML written: ${fileURLToPath(path)} (${Math.round(html.length/1024)} KB)`);
