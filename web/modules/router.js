const defaults = {period:'today',host:'all',gran:'auto',source:'site',page:'1',page_size:'50',sort:'total',order:'desc'};
export function parseRoute(hash) {
  const url = new URL(hash.replace(/^#/, '') || '/overview', 'https://meterly.invalid');
  return {path:url.pathname, state:{...defaults,...Object.fromEntries(url.searchParams)}};
}
export function href(path, state, patch = {}) {
  const q = new URLSearchParams();
  for (const [key,value] of Object.entries({...state,...patch})) if (value !== '' && value !== null && value !== undefined) q.set(key, value);
  return `#${path}?${q}`;
}
export function navigate(path, patch = {}) {const route=parseRoute(location.hash);location.hash=href(path,route.state,patch).slice(1);}
export function update(patch) {const route=parseRoute(location.hash);navigate(route.path,patch);}
export function startRouter(render) {
  const scroll = new Map(); let current=location.hash;
  const changed=()=>{scroll.set(current,window.scrollY);current=location.hash;render(parseRoute(current),scroll.get(current)||0);};
  window.addEventListener('hashchange',changed);render(parseRoute(current),0);
}
