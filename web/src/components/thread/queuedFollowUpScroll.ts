export function followUpScrollTop(
  viewport: { scrollTop: number; clientHeight: number },
  item: { offsetTop: number; height: number }
): number | null {
  if (item.offsetTop < viewport.scrollTop) return item.offsetTop;
  if (item.offsetTop + item.height > viewport.scrollTop + viewport.clientHeight) {
    return item.offsetTop + item.height - viewport.clientHeight;
  }
  return null;
}
