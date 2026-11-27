import createClient from 'openapi-fetch';
import type { paths, components } from './api.gen';

export type User = components['schemas']['User'];
export type Session = components['schemas']['Session'];
export type LoginRequest = components['schemas']['LoginRequest'];
export type Project = components['schemas']['Project'];
export type CreateProjectRequest =
  components['schemas']['CreateProjectRequest'];
export type ProjectMember = components['schemas']['ProjectMember'];
export type ProjectMemberRole = components['schemas']['ProjectMemberRole'];
export type UpsertProjectMemberRequest =
  components['schemas']['UpsertProjectMemberRequest'];
const client = createClient<paths>();

export async function session() {
  const { data, error, response } = await client.GET('/api/v1/auth/session');
  const status = response.status;
  if (response.status === 401) return null;
  if (error) throw new Error(error.message);
  if (!data) throw new Error(`会话查询失败，状态码 ${status}`);
  return data;
}

export async function login(body: LoginRequest) {
  const { data, error } = await client.POST('/api/v1/auth/login', {
    body,
  });
  if (error) throw new Error(error.message);
  if (!data) throw new Error('登录成功但缺少数据');
  return data;
}

export async function logout() {
  const { response } = await client.POST('/api/v1/auth/logout');
  if (!response.ok) throw new Error(`退出登录失败，状态码 ${response.status}`);
}

export async function environment() {
  const { data, error, response } = await client.GET('/api/v1/environment');
  const status = response.status;
  if (error) throw new Error(error.message);
  if (!data) throw new Error(`环境查询失败，状态码 ${status}`);
  return data;
}

export async function version() {
  const { data, response } = await client.GET('/api/v1/version');
  if (!data) throw new Error(`版本查询失败，状态码 ${response.status}`);
  return data;
}

export async function projects() {
  const { data, error, response } = await client.GET('/api/v1/projects');
  const status = response.status;
  if (error) throw new Error(error.message);
  if (!data) throw new Error(`项目列表查询失败，状态码 ${status}`);
  return data;
}

export async function createProject(body: CreateProjectRequest) {
  const { data, error, response } = await client.POST('/api/v1/projects', {
    body,
  });
  const status = response.status;
  if (error) throw new Error(error.message);
  if (!data) throw new Error(`创建项目失败，状态码 ${status}`);
  return data;
}

export type Operation = components['schemas']['Operation'];

export async function projectOperations(id: string) {
  const { data, error, response } = await client.GET(
    '/api/v1/projects/{projectID}/operations',
    { params: { path: { projectID: id } } },
  );
  const status = response.status;
  if (error) throw new Error(error.message);
  if (!data) throw new Error(`操作列表查询失败，状态码 ${status}`);
  return data;
}

export async function project(id: string) {
  const { data, error, response } = await client.GET(
    '/api/v1/projects/{projectID}',
    {
      params: { path: { projectID: id } },
    },
  );
  const status = response.status;
  if (response.status === 404) return null;
  if (error) throw new Error(error.message);
  if (!data) throw new Error(`项目查询失败，状态码 ${status}`);
  return data;
}

export async function projectMembers(id: string) {
  const { data, error, response } = await client.GET(
    '/api/v1/projects/{projectID}/members',
    {
      params: { path: { projectID: id } },
    },
  );
  const status = response.status;
  if (error) throw new Error(error.message);
  if (!data) throw new Error(`项目成员查询失败，状态码 ${status}`);
  return data;
}

export async function upsertProjectMember(
  id: string,
  body: UpsertProjectMemberRequest,
) {
  const { data, error, response } = await client.POST(
    '/api/v1/projects/{projectID}/members',
    {
      params: { path: { projectID: id } },
      body,
    },
  );
  const status = response.status;
  if (error) throw new Error(error.message);
  if (!data) throw new Error(`成员保存失败，状态码 ${status}`);
  return data;
}

export type BrowserNavigation = components['schemas']['BrowserNavigation'];

export class BrowserNavigationError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

export async function createBrowserSession(projectID: string) {
  const { data, error, response } = await client.POST(
    '/api/v1/projects/{projectID}/browser-sessions',
    { params: { path: { projectID } } },
  );
  const status = response.status;
  if (error) throw new Error(error.message);
  if (!data) throw new Error('创建浏览器会话失败，状态码 ' + status);
  return data;
}

export async function navigateBrowserSession(
  projectID: string,
  sessionID: string,
  url: string,
) {
  const { data, error, response } = await client.POST(
    '/api/v1/projects/{projectID}/browser-sessions/{sessionID}/navigate',
    { params: { path: { projectID, sessionID } }, body: { url } },
  );
  if (!data) {
    throw new BrowserNavigationError(
      response.status,
      error?.message ?? '浏览器导航失败，状态码 ' + response.status,
    );
  }
  return data;
}
