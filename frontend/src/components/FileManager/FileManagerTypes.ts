/** Opaque provider reference and display name used by FileManager navigation. */
export type FileManagerBreadcrumb = {
  ref: string | null;
  name: string;
};

export type SortField = 'name' | 'size' | 'modified';
export type SortDirection = 'asc' | 'desc';
