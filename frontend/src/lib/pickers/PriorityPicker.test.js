import { render, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { workspaces } from '../api/workspaces.js';
import { configurationSets, priorities } from '../api/configuration.js';



vi.mock('../api/workspaces.js', () => ({
  workspaces: {
        get: vi.fn(),
        // The workspace-priorities endpoint owns configuration-set resolution;
        // the picker no longer reimplements it.
        getPriorities: vi.fn(),
        getEffectiveConfig: vi.fn(),
      },
}))

vi.mock('../api/configuration.js', () => ({
  configurationSets: { get: vi.fn() },
  priorities: { getAll: vi.fn() },
}));

vi.mock('../stores/i18n.svelte.js', () => ({
  t: (key) => key,
}));


const { default: PriorityPicker } = await import('./PriorityPicker.svelte');

describe('PriorityPicker', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('loads the target workspace priority catalog through the workspace API', async () => {
    workspaces.getPriorities.mockResolvedValue([{ id: 3, name: 'Medium', sort_order: 0 }]);

    render(PriorityPicker, { props: { workspaceId: 9 } });

    await waitFor(() => {
      expect(workspaces.getPriorities).toHaveBeenCalledWith(9);
    });
    // The global priority list is not consulted; the workspace endpoint already
    // resolves the effective configuration set.
    expect(priorities.getAll).not.toHaveBeenCalled();
  });
});
