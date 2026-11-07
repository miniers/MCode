import { formatTurnDuration } from './TurnFinishedBar';

function assertEqual(actual: string, expected: string): void {
  if (actual !== expected) throw new Error(`期望 ${expected}，实际为 ${actual}`);
}

assertEqual(formatTurnDuration(59600), '1 分');
assertEqual(formatTurnDuration(119600), '2 分');
assertEqual(formatTurnDuration(125000), '2 分 5 秒');
assertEqual(formatTurnDuration(500), '不到 1 秒');
