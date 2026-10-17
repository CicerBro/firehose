// Dropdown behaviour for <details class="dropdown"> popovers (Share menu, Advanced settings):
// Escape closes and returns focus to the trigger, a click or focus outside closes, and for an
// action menu (role="menu") the arrow keys, Home and End move between the menu items.

const open = new Set<HTMLDetailsElement>();

function close(d: HTMLDetailsElement, refocus: boolean): void {
  d.open = false;
  if (refocus) d.querySelector<HTMLElement>('summary')?.focus();
}

// 'click', not 'pointerdown': closing a panel can shrink the page and scroll it, which would make
// the pointer miss whatever it was pressed on (e.g. the Start button) before the click lands.
document.addEventListener('click', (ev) => {
  for (const d of open) if (!d.contains(ev.target as Node)) close(d, false);
});
// Keyboard focus leaving a panel (Tab) closes it. Focus moved by a pointer press is left to the
// click handler above, for the same reason.
let pointerDown = false;
document.addEventListener('pointerdown', () => (pointerDown = true), true);
document.addEventListener('pointerup', () => setTimeout(() => (pointerDown = false)), true);
document.addEventListener('pointercancel', () => (pointerDown = false), true);
document.addEventListener('focusin', (ev) => {
  if (pointerDown) return;
  for (const d of open) if (!d.contains(ev.target as Node)) close(d, false);
});
document.addEventListener('keydown', (ev) => {
  if (ev.key !== 'Escape') return;
  for (const d of open) {
    ev.preventDefault();
    close(d, d.contains(document.activeElement));
  }
});

export function initDropdown(d: HTMLDetailsElement): void {
  const summary = d.querySelector<HTMLElement>('summary')!;
  const menu = d.querySelector<HTMLElement>('[role="menu"]');
  const items = (): HTMLElement[] =>
    menu ? [...menu.querySelectorAll<HTMLElement>('[role="menuitem"]:not(:disabled)')] : [];

  let keyAt = -Infinity; // last Enter/Space on the trigger
  d.addEventListener('toggle', () => {
    if (d.open) {
      open.add(d);
      // Opened from the keyboard: move focus into the menu, as a native menu would.
      if (menu && performance.now() - keyAt < 500) items()[0]?.focus();
    } else {
      open.delete(d);
    }
  });

  if (!menu) return;
  summary.addEventListener('keydown', (ev) => {
    if (ev.key === 'Enter' || ev.key === ' ') keyAt = performance.now();
    if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
      ev.preventDefault();
      if (!d.open) d.open = true;
      const list = items();
      (ev.key === 'ArrowDown' ? list[0] : list[list.length - 1])?.focus();
    }
  });
  menu.addEventListener('keydown', (ev) => {
    const list = items();
    if (!list.length) return;
    const i = list.indexOf(document.activeElement as HTMLElement);
    let next: number | null = null;
    if (ev.key === 'ArrowDown') next = (i + 1) % list.length;
    else if (ev.key === 'ArrowUp') next = (i - 1 + list.length) % list.length;
    else if (ev.key === 'Home') next = 0;
    else if (ev.key === 'End') next = list.length - 1;
    if (next !== null) {
      ev.preventDefault();
      list[next]!.focus();
    }
  });
}
