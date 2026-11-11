export class APIError extends Error { constructor(message, status) { super(message); this.status = status; } }
export async function api(path, {signal, method = 'GET', body} = {}) {
  const response = await fetch(`/api/v2${path}`, {credentials: 'same-origin', signal, method,
    headers: body === undefined ? {} : {'Content-Type': 'application/json'}, body: body === undefined ? undefined : JSON.stringify(body)});
  let data; try { data = await response.json(); } catch { throw new APIError('服务返回了无效响应，请重试', response.status); }
  if (!response.ok) throw new APIError(data.error || `请求失败 (${response.status})`, response.status);
  return data;
}
export function query(state, extra = {}) {
  const values = {...state, ...extra};
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(values)) if (value !== undefined && value !== null && value !== '') q.set(key, value);
  return q.toString();
}
