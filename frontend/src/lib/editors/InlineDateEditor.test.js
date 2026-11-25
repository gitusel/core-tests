import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, test, vi } from 'vitest';

vi.mock('../stores/i18n.svelte.js', () => ({
  i18n: { locale: 'en-US' },
  t: (key) => key,
}));

import InlineDateEditor from './InlineDateEditor.svelte';

describe('InlineDateEditor', () => {
  test('saves the date-only value required by the item update endpoint', async () => {
    const onsave = vi.fn();
    const { container } = render(InlineDateEditor, {
      props: { value: '2026-08-12T00:00:00Z', onsave },
    });

    await fireEvent.click(screen.getByRole('button'));

    // The editor's date field is the shared DatePicker: the stored timestamp
    // projects to a date-only calendar, and a picked day commits date-only.
    await fireEvent.click(await screen.findByTestId('inline-date-input'));
    const calendar = await screen.findByTestId('inline-date-input-calendar');
    expect(calendar).toHaveAttribute('data-calendar-start', '2026-08-12');
    expect(calendar.querySelector('[aria-selected="true"]')).toHaveAttribute(
      'data-value',
      '2026-08-12',
    );

    await fireEvent.click(await screen.findByTestId('inline-date-input-calendar-day-2026-08-20'));
    await fireEvent.keyDown(container.querySelector('[role="group"]'), { key: 'Enter' });

    expect(onsave).toHaveBeenCalledWith({ value: '2026-08-20' });
  });
});
