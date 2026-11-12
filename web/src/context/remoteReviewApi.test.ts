// 无框架测试：web/ 没有测试运行器，顶层断言失败即抛错。
import { reviewContentHash, reviewHunkHash } from './remoteReviewApi';

function assertEqual<T>(actual: T, expected: T, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

const lf = String.fromCharCode(10);
const text = ['one', 'TWO', 'three', 'FOUR', 'five', ''].join(lf);
assertEqual(
  await reviewContentHash(text),
  '19aa528fb65964f783c0e25358fd35b1b5bd1548b778f4e384f9e5058e3b3bf2',
  '审阅内容哈希'
);
assertEqual(
  await reviewHunkHash(text, 2, 1),
  '465a43c7b7b79945ec5bc4dd80b20230ea1a992bd6401fe2ed5f736d67799e0c',
  '审阅块哈希'
);

assertEqual(
  await reviewHunkHash(['one', 'TWO'].join(lf), 2, 1),
  'a1a8a8cbbed4eb53ae62ee4fb0787504087232c29aa4d817757d06b68d0501ca',
  '末行无换行符时的审阅块哈希'
);
