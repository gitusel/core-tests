import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('../../stores/i18n.svelte.js', () => ({ t: (key) => key }));

import SidebarDateField from './SidebarDateField.svelte';

afterEach(cleanup);

function testId(fieldKey) {
  return `sidebar-date-${fieldKey.replaceAll('_', '-')}`;
}

// The inline editor is the shared DatePicker, whose inline calendar grid is
// keyed off the field's own testid.
async function startEditing(fieldKey = 'due_date') {
  await fireEvent.click(screen.getByTestId(testId(fieldKey)));
  return screen.findByTestId(`${testId(fieldKey)}-input-root`);
}

async function pickDay(fieldKey, day) {
  const cell = await screen.findByTestId(`${testId(fieldKey)}-input-calendar-day-${day}`);
  await fireEvent.click(cell);
}

describe('SidebarDateField', () => {
  it('displays a date and starts editing when enabled', async () => {
    const onStartEdit = vi.fn();
    render(SidebarDateField, {
      props: {
        fieldKey: 'due_date',
        label: 'Due date',
        value: '2026-09-12T00:00:00Z',
        editable: true,
        onStartEdit,
      },
    });

    const field = screen.getByTestId('sidebar-date-due-date');
    expect(field).toHaveTextContent('Due date');
    expect(field).not.toHaveTextContent('common.none');

    await fireEvent.click(field);
    expect(onStartEdit).toHaveBeenCalledOnce();
    expect(await screen.findByTestId('sidebar-date-due-date-input-root')).toBeInTheDocument();
    expect(screen.getByTestId('sidebar-date-due-date-input-calendar')).toBeInTheDocument();
  });

  it('saves a changed value and closes the editor', async () => {
    const onSave = vi.fn();
    render(SidebarDateField, {
      props: {
        fieldKey: 'start_date',
        label: 'Start date',
        value: '2026-09-12T00:00:00Z',
        editable: true,
        onSave,
      },
    });

    await startEditing('start_date');
    await pickDay('start_date', '2026-09-15');

    expect(onSave).toHaveBeenCalledWith('2026-09-15');
    expect(screen.queryByTestId('sidebar-date-start-date-input-root')).not.toBeInTheDocument();
  });

  it('saves a cleared value', async () => {
    const onSave = vi.fn();
    render(SidebarDateField, {
      props: {
        fieldKey: 'due_date',
        label: 'Due date',
        value: '2026-09-12T00:00:00Z',
        editable: true,
        onSave,
      },
    });

    await startEditing('due_date');
    await fireEvent.click(screen.getByTestId('sidebar-date-due-date-input-footer-clear'));

    expect(onSave).toHaveBeenCalledWith(null);
  });

  it('cancels editing when clicking outside', async () => {
    const onCancel = vi.fn();
    render(SidebarDateField, {
      props: {
        fieldKey: 'end_date',
        label: 'End date',
        editable: true,
        onCancel,
      },
    });

    await startEditing('end_date');
    await fireEvent.click(document.body);

    expect(onCancel).toHaveBeenCalledOnce();
    expect(screen.queryByTestId('sidebar-date-end-date-input-root')).not.toBeInTheDocument();
  });

  it('does not start editing when disabled', async () => {
    const onStartEdit = vi.fn();
    render(SidebarDateField, {
      props: {
        fieldKey: 'due_date',
        label: 'Due date',
        editable: false,
        onStartEdit,
      },
    });

    const field = screen.getByTestId('sidebar-date-due-date');
    expect(field).toBeDisabled();
    await fireEvent.click(field);
    expect(onStartEdit).not.toHaveBeenCalled();
  });
});
