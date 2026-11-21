import React, { useEffect, useRef } from 'react';

export const SHEET_LAYER_CLASS = 'sheet-layer';
export const SHEET_MENU_CARD_CLASS = 'thread-action-sheet';
export const SHEET_PANEL_CARD_CLASS =
  'settings-sheet flex flex-col max-h-[calc(var(--app-height,100dvh)*0.92)] outline-none w-full max-w-full min-w-0 overflow-hidden';

export function pushSheet<T>(stack: readonly T[], item: T): T[] { return [...stack, item]; }
export function removeSheet<T>(stack: readonly T[], item: T): T[] { return stack.filter((entry) => entry !== item); }
export function isTopSheet<T>(stack: readonly T[], item: T): boolean { return stack[stack.length - 1] === item; }
export function nextFocusIndex(index: number, count: number, backwards: boolean): number {
  if (count === 0) return -1;
  if (index < 0 || index >= count) return backwards ? count - 1 : 0;
  return (index + (backwards ? -1 : 1) + count) % count;
}

let sheetStack: object[] = [];
const focusableSelector = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

export interface SheetFrameProps {
  onRequestClose: () => void;
  labelledBy?: string;
  ariaLabel?: string;
  onFocus?: React.FocusEventHandler<HTMLElement>;
  size?: 'menu' | 'panel' | 'bare';
  cardRef?: React.Ref<HTMLElement>;
  cardStyle?: React.CSSProperties;
  layerStyle?: React.CSSProperties;
  cardClassName?: string;
  layerClassName?: string;
  role?: 'dialog' | 'alertdialog';
  closeOnEscape?: boolean;
  layerChildren?: React.ReactNode;
  children: React.ReactNode;
}

export function SheetFrame({ onRequestClose, labelledBy, ariaLabel, onFocus, size = 'panel', cardClassName, cardRef: externalRef, cardStyle, layerStyle, layerClassName, role = 'dialog', closeOnEscape = true, layerChildren, children }: SheetFrameProps) {
  const cardRef = useRef<HTMLElement | null>(null);
  const layerRef = useRef<HTMLDivElement | null>(null);
  const identity = useRef({});
  const closeRef = useRef(onRequestClose);
  const escapeRef = useRef(closeOnEscape);
  closeRef.current = onRequestClose;
  escapeRef.current = closeOnEscape;

  useEffect(() => {
    const card = cardRef.current;
    const layer = layerRef.current;
    if (!card || !layer) return;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const item = identity.current;
    sheetStack = pushSheet(sheetStack, item);
    const focusable = () => Array.from(layer.querySelectorAll<HTMLElement>(focusableSelector)).filter((element) => element.getClientRects().length > 0);
    if (!layer.contains(document.activeElement)) (focusable()[0] ?? card).focus();
    const handleKeyDown = (event: KeyboardEvent) => {
      if (!isTopSheet(sheetStack, item)) return;
      if (event.key === 'Escape' && escapeRef.current) {
        event.stopPropagation();
        closeRef.current();
      } else if (event.key === 'Tab') {
        const elements = focusable();
        const next = nextFocusIndex(elements.indexOf(document.activeElement as HTMLElement), elements.length, event.shiftKey);
        event.preventDefault();
        (elements[next] ?? card).focus();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => {
      window.removeEventListener('keydown', handleKeyDown);
      sheetStack = removeSheet(sheetStack, item);
      if (previous?.isConnected) previous.focus();
    };
  }, []);

  const cardClass = [size === 'bare' ? undefined : size === 'menu' ? SHEET_MENU_CARD_CLASS : SHEET_PANEL_CARD_CLASS, cardClassName].filter(Boolean).join(' ');
  return (
    <div ref={layerRef} className={[SHEET_LAYER_CLASS, layerClassName].filter(Boolean).join(' ')} style={layerStyle} role="presentation" onMouseDown={onRequestClose}>
      <section ref={(node) => { cardRef.current = node; if (typeof externalRef === 'function') externalRef(node); else if (externalRef) (externalRef as React.MutableRefObject<HTMLElement | null>).current = node; }} className={cardClass} style={cardStyle} role={role} aria-modal="true" aria-labelledby={labelledBy} aria-label={ariaLabel} onFocus={onFocus} tabIndex={-1} onMouseDown={(event) => event.stopPropagation()}>
        {children}
      </section>
      {layerChildren}
    </div>
  );
}
