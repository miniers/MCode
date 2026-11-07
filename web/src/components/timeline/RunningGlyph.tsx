import React from 'react';

// 3×3 低频点阵：对角线依次亮起，一圈 1.4 秒，只用于「运行中」这类持续状态。
// 参考桌面端 internal/ui/sidebar_bees.go 的 renderRunningGlyph，但改用 CSS 动画，避免每帧重绘。
const GLYPH_KEYFRAMES = `@keyframes mcode-timeline-running-glyph {
  0%, 100% { opacity: 0.22; transform: scale(0.82); }
  45% { opacity: 1; transform: scale(1); }
}
@media (prefers-reduced-motion: reduce) {
  .mcode-timeline-running-glyph-dot { animation: none !important; opacity: 0.6 !important; transform: none !important; }
}`;

const GLYPH_PERIOD = '1.4s';

const CELLS = Array.from({ length: 9 }, (_, index) => ({
  row: Math.floor(index / 3),
  col: index % 3
}));

export const RunningGlyph: React.FC<{ size?: number; label?: string }> = ({
  size = 12,
  label = '运行中'
}) => (
  <span
    role="img"
    aria-label={label}
    className="inline-flex shrink-0 items-center justify-center"
    style={{ width: size, height: size }}
  >
    <style>{GLYPH_KEYFRAMES}</style>
    <span
      className="grid"
      style={{
        gridTemplateColumns: 'repeat(3, 1fr)',
        gridTemplateRows: 'repeat(3, 1fr)',
        gap: Math.max(1, size * 0.08),
        width: size,
        height: size
      }}
    >
      {CELLS.map(({ row, col }) => (
        <span
          key={`${row}-${col}`}
          className="mcode-timeline-running-glyph-dot block rounded-full"
          style={{
            backgroundColor: 'var(--primary)',
            opacity: 0.22,
            animation: `mcode-timeline-running-glyph ${GLYPH_PERIOD} ease-in-out infinite`,
            // 对角线序号 0..4 摊到一整圈里，宽点之后依次衰减
            animationDelay: `${(((row + col) / 4) * 1.4).toFixed(2)}s`
          }}
        />
      ))}
    </span>
  </span>
);

RunningGlyph.displayName = 'RunningGlyph';
