import { apiJson, type ApiJsonResult } from '../utils/apiClient';

export type ErrorListItem = {
  id: string;
  kind: 'transfer' | 'indexing';
  resource_type: string;
  path: string;
  status: string;
  attempts: number;
  error_message: string;
  occurred_at: string;
};

export type ErrorListResponse = {
  errors: ErrorListItem[];
  total: number;
};

export function listErrors(request: {
  endpoint: string;
  token: string;
  limit: number;
  offset: number;
}): Promise<ApiJsonResult<ErrorListResponse>> {
  return apiJson<ErrorListResponse>(`${request.endpoint}?limit=${request.limit}&offset=${request.offset}`, {
    headers: { Authorization: `Bearer ${request.token}` },
  });
}
