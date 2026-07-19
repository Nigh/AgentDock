async function req(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401) throw new ApiError('unauthorized', 401);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(data.error || res.statusText, res.status);
  return data;
}

export class ApiError extends Error {
  constructor(message, status) {
    super(message);
    this.status = status;
  }
}

export const api = {
  me: () => req('GET', '/api/me'),
  register: (username, password) => req('POST', '/api/register', { username, password }),
  login: (username, password) => req('POST', '/api/login', { username, password }),
  logout: () => req('POST', '/api/logout'),
  nodeTokens: () => req('GET', '/api/me/node-tokens'),
  createNodeToken: (name) => req('POST', '/api/me/node-tokens', { name }),
  deleteNodeToken: (id) => req('DELETE', `/api/me/node-tokens/${id}`),
  state: () => req('GET', '/api/state'),
  listUsers: () => req('GET', '/api/users'),
  approveUser: (uid) => req('POST', `/api/users/${uid}/approve`),
  deleteUser: (uid) => req('DELETE', `/api/users/${uid}`),
  shareNode: (nodeId, uid) => req('POST', `/api/nodes/${nodeId}/share`, { uid }),
  revokeShare: (nodeId, uid) => req('DELETE', `/api/nodes/${nodeId}/share/${uid}`),
  createDirectory: (name, path) => req('POST', '/api/directories', { name, path }),
  deleteDirectory: (id) => req('DELETE', `/api/directories/${id}`),
  browse: (nodeId, path) => req('GET', `/api/browse?node_id=${nodeId}&path=${encodeURIComponent(path || '')}`),
  createSession: ({ name, nodeId = 0, cwd = '', shell = '', fromSession = '' }) =>
    req('POST', '/api/sessions', { name, cwd, shell, node_id: nodeId, from_session: fromSession }),
  killSession: (id) => req('DELETE', `/api/sessions/${id}`),
};

export function terminalWsUrl(sessionId) {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${location.host}/api/sessions/${sessionId}/ws`;
}
