import { render, screen } from '@testing-library/svelte';
import { writable } from 'svelte/store';
import { afterEach, describe, expect, test, vi } from 'vitest';

const listPacks = vi.hoisted(() => vi.fn());
const applyPack = vi.hoisted(() => vi.fn());
const verifyPack = vi.hoisted(() => vi.fn());

vi.mock('../api/packs.js', () => ({
  packs: { list: listPacks, apply: applyPack, verify: verifyPack },
}));

vi.mock('../stores/workspaces.svelte.js', async () => {
  const { writable: writableStore } = await import('svelte/store');
  return {
    workspacesStore: Object.assign(
          writableStore({ regularWorkspaces: [{ id: 4, name: 'Support', key: 'SUP' }] }),
          { load: vi.fn(), reload: vi.fn() },
        ),
  };
});

vi.mock('../stores/i18n.svelte.js', () => ({
  t: (key) => key,
}));

vi.mock('../stores/toasts.svelte.js', () => ({
  surfaceError: vi.fn(),
  errorToast: vi.fn(),
  successToast: vi.fn(),
}));

import PackManager from './PackManager.svelte';
import { packs } from '../api/packs.js';

afterEach(() => {
  document.body.innerHTML = '';
  vi.clearAllMocks();
});

// PackManager is a read-only catalog: packs are applied by selecting one in the
// workspace creation form (CreateModal -> WorkspaceForm template picker), so
// this screen only reports what the server ships.
describe('PackManager', () => {
  test('lists the built-in packs with their version, content flag, and plugins', async () => {
    listPacks.mockResolvedValue([
      {
        name: 'helpdesk',
        version: '1.0.0',
        description: 'Support',
        has_content: true,
        plugins: ['support-metrics'],
      },
    ]);

    render(PackManager);

    const row = await screen.findByTestId('pack-row-helpdesk');
    expect(row).toHaveTextContent('helpdesk');
    expect(row).toHaveTextContent('1.0.0');
    expect(row).toHaveTextContent('settings.featurePacks.content');
    expect(row).toHaveTextContent('Support');
    expect(row).toHaveTextContent('support-metrics');
    // No apply surface remains here.
    expect(applyPack).not.toHaveBeenCalled();
    expect(screen.queryByTestId('pack-apply-helpdesk')).not.toBeInTheDocument();
  });
});
