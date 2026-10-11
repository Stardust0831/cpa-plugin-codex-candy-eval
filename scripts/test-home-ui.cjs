const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const context = vm.createContext({
  window: { CPA_HOME_EVAL: true },
  localStorage: { getItem: () => JSON.stringify({ state: { managementKey: 'must-not-reuse-home-key' } }), setItem() {} },
  Date, TextEncoder, AbortSignal, URLSearchParams,
});
const files = ['catalog.js', 'components.js', 'app.js'];
const source = files.map(file => fs.readFileSync(path.join(__dirname, '../internal/plugin/web', file), 'utf8').split('// Events and initialization.')[0]).join('\n');
vm.runInContext(source, context);
const run = code => vm.runInContext(code, context);

(async () => {
  assert.equal(run('panelKey()'), '', 'Home management key must never be reused for panel login');
  assert.equal(run('switchable({source:"auth_files",id:"uuid-a"})'), false, 'Home account controls are read-only');
  const requests = [];
  context.fetch = async (route, options) => {
    requests.push({ route, options });
    const body = route === '/api/home/catalog' ? { models: [{ id: 'gpt-test' }, { id: 'gpt-image-test' }] } : { models: [{ id: 'gpt-test' }] };
    return { ok: true, status: 200, text: async () => JSON.stringify(body) };
  };
  run(`key='synthetic-panel-key'; resetCatalogCache({owner:'test',credentials:Object.create(null)});`);
  await run('refreshCatalog({force:true})');
  assert.equal(requests.length, 1);
  assert.equal(requests[0].route, '/api/home/catalog');
  assert.equal(requests[0].options.headers.Authorization, 'Bearer synthetic-panel-key');
  assert.deepEqual(JSON.parse(run('JSON.stringify(catalogCache.models.ids)')), ['gpt-test']);
  await run(`credentialModels('uuid-a')`);
  assert.equal(requests[1].route, '/v0/management/auth-files/models?name=uuid-a');
  context.fetch = async () => ({ ok: false, status: 401, text: async () => '{}' });
  await assert.rejects(run(`api('/api/home/catalog')`), /面板访问密钥/);
  console.log('Home UI checks passed');
})().catch(error => { console.error(error); process.exitCode = 1; });
