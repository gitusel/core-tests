import { render, screen } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  getPage: vi.fn(),
  storeLoad: vi.fn(),
  storeRemove: vi.fn(),
}));

vi.mock('../api/workspaces.js', () => ({
  workspaces: {
        getPage: mocks.getPage,
      },
}));

vi.mock('../stores/i18n.svelte.js', () => ({
  t: (key) =>
    ({
      'workspaces.restricted': 'Restricted',
      'workspaces.template': 'Template',
      'workspaces.personal': 'Personal',
      'workspaces.visibility': 'Visibility',
      'workspaces.open': 'Open',
    })[key] ?? key,
}));

vi.mock('../stores/workspaces.svelte.js', async () => {
  const { writable } = await import('svelte/store');
  return {
    workspacesStore: {
          subscribe: writable([]).subscribe,
          load: mocks.storeLoad,
          remove: mocks.storeRemove,
        },
  };
})

vi.mock('../stores/permissions.svelte.js', async () => {
  const { writable } = await import('svelte/store');
  return {
    permissionStore: {
          subscribe: writable({ userPermissionKeys: new Set(['workspace.create']) }).subscribe,
        },
    isSystemAdmin: writable(false),
  };
});

vi.mock('../router.js', () => ({ navigate: vi.fn() }));
vi.mock('../composables/useConfirm.js', () => ({ confirm: vi.fn() }));
vi.mock('../stores/toasts.svelte.js', () => ({ surfaceError: vi.fn(), errorToast: vi.fn() }));
vi.mock('../utils/keyboardShortcuts.js', () => ({
  toHotkeyString: () => '',
  getShortcutDisplay: () => '',
}));

import Workspaces from './Workspaces.svelte';
import { workspaces } from '../api/workspaces.js';

function workspaceRow(id, isRestricted) {
  return {
    id,
    name: isRestricted ? 'Locked Space' : 'Open Space',
    key: isRestricted ? 'LOCK' : 'OPEN',
    description: '',
    active: true,
    is_personal: false,
    is_template: false,
    is_restricted: isRestricted,
    icon: 'folder',
    color: '#3b82f6',
    created_at: '2026-01-01T00:00:00Z',
  };
}

describe('Workspaces admin directory restricted badge', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // WI-1446: the admin directory is paged server-side.
    mocks.getPage.mockResolvedValue({
      data: [workspaceRow(1, false), workspaceRow(2, true)],
      pagination: { page: 1, page_size: 50, total_items: 2 },
    });
  });

  it('marks viewer-gated workspaces as restricted and leaves open ones unmarked', async () => {
    render(Workspaces);

    await screen.findByTestId('workspace-row-1');

    // The visibility column sits next to status and carries the badge.
    expect(screen.getByTestId('table-column-visibility')).toBeInTheDocument();
    const restricted = screen.getByTestId('workspace-restricted-badge-2');
    expect(restricted).toHaveTextContent('Restricted');
    expect(screen.queryByTestId('workspace-restricted-badge-1')).toBeNull();
    expect(screen.getByTestId('workspace-open-1')).toHaveTextContent('Open');
  });

  it('loads the full directory from the workspaces API', async () => {
    render(Workspaces);

    await screen.findByTestId('workspace-row-2');
    expect(mocks.getPage).toHaveBeenCalledWith({
      page: 1,
      page_size: 50,
      search: '',
      personal: false,
    });
    expect(mocks.storeLoad).toHaveBeenCalledWith({ force: true });
  });
});
