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
  login: (username, password) => req('POST', '/api/login', { username, password }),
  logout: () => req('POST', '/api/logout'),
  state: () => req('GET', '/api/state'),
  createDirectory: (name, path) => req('POST', '/api/directories', { name, path }),
  deleteDirectory: (id) => req('DELETE', `/api/directories/${id}`),
  createSession: (name, cwd, shell) => req('POST', '/api/sessions', { name, cwd, shell }),
  killSession: (id) => req('DELETE', `/api/sessions/${id}`),
};

export function terminalWsUrl(sessionId) {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${location.host}/api/sessions/${sessionId}/ws`;
}
