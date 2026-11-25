import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { writable } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';

const addMilestone = vi.hoisted(() => vi.fn());
const canAdminWorkspace = vi.hoisted(() => vi.fn());
const hasPermission = vi.hoisted(() => vi.fn());
const navigate = vi.hoisted(() => vi.fn());

vi.mock('../api/workspaces.js', () => ({
  workspaces: {
    create: vi.fn(),
    getTemplates: vi.fn().mockResolvedValue([]),
    // WorkItemForm/WorkspaceDataStore resolve the create screen config.
    getEffectiveConfig: vi.fn().mockResolvedValue(null),
    getPage: vi.fn().mockResolvedValue({ data: [], pagination: { total: 0 } }),
  },
}))

vi.mock('../api/items.js', () => ({
  items: {},
}))

vi.mock('../api/collections.js', () => ({
  collections: {},
  collectionCategories: { getAll: vi.fn().mockResolvedValue([]) },
}))

vi.mock('../api/planning.js', () => ({
  milestones: {},
  milestoneCategories: { getAll: vi.fn().mockResolvedValue([]) },
}))

vi.mock('../api/misc.js', () => ({
  attachments: {},
  // The shared workspace data store primes labels during bootstrap.
  labels: { getAll: vi.fn().mockResolvedValue([]) },
}));

vi.mock('../stores/milestones.js', () => ({ milestonesStore: { add: addMilestone } }));

vi.mock('../router.js', () => ({
  navigate,
  currentRoute: writable({ view: 'milestones', params: {} }),
}));

vi.mock('../stores/workspaces.svelte.js', () => ({
  workspacesStore: Object.assign(writable({ loaded: true, regularWorkspaces: [] }), {
      add: vi.fn(),
      load: vi.fn(),
    }),
}))

vi.mock('../stores/navigationConfig.svelte.js', () => ({
  shouldNavigateAfterCreate: vi.fn(() => false),
}))

vi.mock('../stores/workItemFormStore.svelte.js', async () => {
  // Keep the real store for rendering the form's configured fields, but
  // drive the create flow from the form data this test controls.
  const { workItemFormStore } = await vi.importActual(
    '../stores/workItemFormStore.svelte.js'
  );
  workItemFormStore.formData = { name: '', workspace_id: null };
  workItemFormStore.init = vi.fn();
  workItemFormStore.resetForm = vi.fn();
  workItemFormStore.validate = vi.fn(() => false);
  return { workItemFormStore };
});

vi.mock('../stores/permissions.svelte.js', () => ({
  permissionStore: writable({ userPermissionKeys: new Set(['milestone.create']) }),
  isSystemAdmin: writable(false),
}))

vi.mock('../stores/workspacePermissions.svelte.js', () => ({
  workspacePermissions: { canAdminWorkspace, hasPermission, reload: vi.fn() },
}));

vi.mock('../stores/i18n.svelte.js', () => ({ t: (key) => key }));
vi.mock('../stores/toasts.svelte.js', () => ({ surfaceError: vi.fn(), errorToast: vi.fn() }));
vi.mock('../utils/createdItemToast.js', () => ({ showCreatedItemToast: vi.fn() }));

import CreateModal from './CreateModal.svelte';
import { workspaces } from '../api/workspaces.js';
import { items } from '../api/items.js';
import { collections } from '../api/collections.js';
import { milestones } from '../api/planning.js';
import { attachments } from '../api/misc.js';





beforeEach(() => {
  addMilestone.mockReset().mockResolvedValue({ id: 1, name: 'Release 2' });
  canAdminWorkspace.mockReset().mockReturnValue(false);
  hasPermission.mockReset().mockReturnValue(false);
});

afterEach(() => {
  document.body.innerHTML = '';
  vi.clearAllMocks();
});

// The target-date chip opens the shared inline DatePicker. Its calendar starts
// on the current month, so page to the wanted month before picking the day.
async function pickTargetDate(iso) {
  const calendarTestid = 'milestone-target-date-calendar';
  const targetMonth = iso.slice(0, 7);
  await waitFor(() => {
    if (!document.querySelector(`[data-testid="${calendarTestid}"]`)) {
      throw new Error('target-date calendar not rendered');
    }
  });
  for (let guard = 0; guard < 36; guard += 1) {
    const shown = document
      .querySelector(`[data-testid="${calendarTestid}"]`)
      ?.getAttribute('data-calendar-start')
      ?.slice(0, 7);
    if (shown === targetMonth) break;
    const nav = shown > targetMonth ? 'prev' : 'next';
    await fireEvent.click(document.querySelector(`[data-testid="${calendarTestid}-${nav}"]`));
  }
  await fireEvent.click(await screen.findByTestId(`${calendarTestid}-day-${iso}`));
}

async function submitMilestone(props) {
  render(CreateModal, {
    props: { isOpen: true, initialType: 'milestone', skipNavigate: true, ...props },
  });

  await fireEvent.input(document.querySelector('input[placeholder="createModal.milestoneName"]'), {
    target: { value: 'Release 2' },
  });
  const dateChip = [...document.querySelectorAll('button')].find((button) =>
    button.textContent?.includes('createModal.targetDate')
  );
  await fireEvent.click(dateChip);
  await pickTargetDate('2026-12-01');

  await waitFor(() => expect(document.querySelector('#create-modal-submit').disabled).toBe(false));
  await fireEvent.click(document.querySelector('#create-modal-submit'));
  await waitFor(() => expect(addMilestone).toHaveBeenCalledTimes(1));
  return addMilestone.mock.calls[0][0];
}

describe('CreateModal milestone scope', () => {
  test('scopes to the opening workspace when the user may write there', async () => {
    hasPermission.mockReturnValue(true);

    const payload = await submitMilestone({ initialWorkspaceId: 42 });

    expect(payload.is_global).toBe(false);
    expect(payload.workspace_id).toBe(42);
    expect(payload.name).toBe('Release 2');
    expect(payload.target_date).toBe('2026-12-01');
  });

  test('creates a global milestone without a workspace context', async () => {
    const payload = await submitMilestone({});

    expect(payload.is_global).toBe(true);
    expect(payload.workspace_id).toBeNull();
  });

  test('falls back to a global milestone when the user cannot write in the workspace', async () => {
    canAdminWorkspace.mockReturnValue(false);
    hasPermission.mockReturnValue(false);

    const payload = await submitMilestone({ initialWorkspaceId: 42 });

    expect(payload.is_global).toBe(true);
    expect(payload.workspace_id).toBeNull();
  });
});
