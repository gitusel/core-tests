import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';

vi.mock('../stores/i18n.svelte.js', () => ({ t: (key) => key }));

beforeAll(() => {
  if (!globalThis.ResizeObserver) {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    };
  }
});

import DatePicker from './DatePicker.svelte';

// DatePicker lazy-loads the non-Gregorian field; warm the module cache so the
// Persian test does not race the dynamic import under full-suite load.
beforeAll(async () => {
  await import('./CalendarDateField.svelte');
});

afterEach(() => {
  cleanup();
  document.body.innerHTML = '';
});

describe('DatePicker', () => {
  it('renders a Gregorian trigger and commits a picked day', async () => {
    const onchange = vi.fn();
    render(DatePicker, {
      props: { value: '2026-11-07', locale: 'en', dataTestid: 'dp', onchange }
    });

    // Every calendar system now shares the same combobox trigger + popover
    // calendar; only the locale's calendar differs.
    const trigger = screen.getByTestId('dp');
    expect(trigger.tagName).toBe('BUTTON');
    expect(trigger).toHaveTextContent('November 7, 2026');

    await fireEvent.click(trigger);

    const day = await screen.findByTestId('dp-calendar-day-2026-11-20');
    await fireEvent.click(day);

    expect(onchange).toHaveBeenCalledWith('2026-11-20');
  });

  it('renders a Jalali trigger and calendar for Persian locales', async () => {
    const onchange = vi.fn();
    render(DatePicker, {
      props: { value: '2026-11-07', locale: 'fa', dataTestid: 'dp', onchange }
    });

    const trigger = await screen.findByTestId('dp');
    expect(trigger.tagName).toBe('BUTTON');
    expect(trigger).toHaveTextContent('۱۶ آبان ۱۴۰۵');

    await fireEvent.click(trigger);

    const day = await screen.findByTestId('dp-calendar-day-2026-11-20');
    await fireEvent.click(day);

    expect(onchange).toHaveBeenCalledTimes(1);
    expect(onchange).toHaveBeenCalledWith('2026-11-20');
  });
});
